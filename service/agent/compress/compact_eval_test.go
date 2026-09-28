package compress

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"edu.agent.code/config"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// 本文件是独立于实现者的成效评估：只新增文件，不修改任何实现代码。
//
// 评估口径说明：
//  1. token 数使用 estimateTokens（约 4 runes/token）估算，与 Eino 在缺少真实
//     usage 时的兜底口径一致，只用于「压缩前 vs 压缩后」的相对比较。
//  2. 摘要模型使用本地 stub，不发起任何网络请求；压缩耗时因此只是中间件自身
//     开销，不含真实模型的网络与推理延迟。
//  3. 压缩入口走真实中间件方法 BeforeModelRewriteState，因此触发判断本身也被覆盖。

const evalSummaryTag = "<all_user_messages>\n</all_user_messages>"

// countingSummaryModel 记录摘要模型被调用的次数与收到的提示，用于量化压缩开销。
type countingSummaryModel struct {
	reply  string
	calls  int
	inputs [][]*schema.Message
}

func (m *countingSummaryModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls++
	m.inputs = append(m.inputs, input)
	return schema.AssistantMessage(m.reply+"\n"+evalSummaryTag, nil), nil
}

func (m *countingSummaryModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, fmt.Errorf("stream is not supported by countingSummaryModel")
}

func evalMiddleware(t *testing.T, conf config.ContextCompact, chatModel model.BaseModel[*schema.Message]) *summarization.TypedMiddleware[*schema.Message] {
	t.Helper()
	middleware, err := New(context.Background(), conf, chatModel)
	if err != nil {
		t.Fatalf("build middleware: %v", err)
	}
	if middleware == nil {
		t.Fatal("middleware is nil although Enabled=true")
	}
	concrete, ok := middleware.(*summarization.TypedMiddleware[*schema.Message])
	if !ok {
		t.Fatalf("middleware type = %T, want *summarization.TypedMiddleware[*schema.Message]", middleware)
	}
	return concrete
}

// runCompaction 走真实入口，压缩未触发时返回原始消息。
func runCompaction(t *testing.T, middleware *summarization.TypedMiddleware[*schema.Message], messages []*schema.Message) []*schema.Message {
	t.Helper()
	state := &adk.ChatModelAgentState{
		Messages:  messages,
		ToolInfos: []*schema.ToolInfo{{Name: "read_files", Desc: "读取文件内容"}},
	}
	_, out, err := middleware.BeforeModelRewriteState(context.Background(), state, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("BeforeModelRewriteState: %v", err)
	}
	if out == nil {
		return nil
	}
	return out.Messages
}

// evalHistory 构造「多轮工具调用 + 大工具输出」的历史，形状与真实长任务一致：
// system 约束在最前，其后是若干轮 (user, assistant tool_call, tool result)，最后是当前问题。
func evalHistory(rounds, runesPerToolResult int) []*schema.Message {
	messages := []*schema.Message{schema.SystemMessage("系统约束：只读分析，禁止破坏性操作，结论必须给出文件路径与行号")}
	repeat := runesPerToolResult / 26
	if repeat < 1 {
		repeat = 1
	}
	for i := 0; i < rounds; i++ {
		callID := fmt.Sprintf("call-%d", i)
		messages = append(messages,
			schema.UserMessage(fmt.Sprintf("第 %d 轮：分析模块 %d 的实现", i+1, i+1)),
			schema.AssistantMessage("", []schema.ToolCall{{
				ID:       callID,
				Type:     "function",
				Function: schema.FunctionCall{Name: "read_files", Arguments: fmt.Sprintf("{\"file\":\"module-%d.go\"}", i+1)},
			}}),
			schema.ToolMessage(strings.Repeat("文件正文片段：func Handler() { /* 实现细节 */ }\n", repeat), callID),
		)
	}
	return append(messages, schema.UserMessage("当前问题：基于上面的分析给出结论与证据"))
}

func reductionPercent(before, after int) float64 {
	if before == 0 {
		return 0
	}
	return 100 * float64(before-after) / float64(before)
}

// TestEvalReductionAtRealisticScales 量化不同规模长历史的压缩成效。
func TestEvalReductionAtRealisticScales(t *testing.T) {
	cases := []struct {
		name     string
		rounds   int
		runesPer int
	}{
		{"6轮x2k", 6, 2000},
		{"6轮x8k", 6, 8000},
		{"12轮x8k", 12, 8000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := evalHistory(tc.rounds, tc.runesPer)
			beforeTokens := estimateTokens(original)
			stub := &countingSummaryModel{reply: "已确认结论：入口在 main.go（证据：main.go:12）。"}
			middleware := evalMiddleware(t, config.ContextCompact{Enabled: true, TriggerTokens: 1000}, stub)

			start := time.Now()
			compacted := runCompaction(t, middleware, original)
			elapsed := time.Since(start)

			afterTokens := estimateTokens(compacted)
			t.Logf("scale=%s messages %d->%d estimated_tokens %d->%d reduction=%.1f%% summary_calls=%d middleware_cost=%s",
				tc.name, len(original), len(compacted), beforeTokens, afterTokens,
				reductionPercent(beforeTokens, afterTokens), stub.calls, elapsed)

			if stub.calls != 1 {
				t.Fatalf("want exactly 1 summary model call, got %d", stub.calls)
			}
			if afterTokens >= beforeTokens {
				t.Fatalf("compaction did not reduce prompt: %d -> %d", beforeTokens, afterTokens)
			}
		})
	}
}

// TestEvalPostCompactionInvariants 检查压缩后必须成立的不变量。
func TestEvalPostCompactionInvariants(t *testing.T) {
	original := evalHistory(8, 4000)
	stub := &countingSummaryModel{reply: "已确认结论：入口在 main.go（证据：main.go:12）。"}
	middleware := evalMiddleware(t, config.ContextCompact{Enabled: true, TriggerTokens: 1000}, stub)

	compacted := runCompaction(t, middleware, original)
	if len(compacted) == 0 {
		t.Fatal("compacted history is empty")
	}
	if len(compacted) >= len(original) {
		t.Fatalf("message count did not drop: %d -> %d", len(original), len(compacted))
	}

	// (a) system 约束必须原样保留，且仍在最前。
	if compacted[0].Role != schema.System || !strings.Contains(compacted[0].Content, "只读分析") {
		t.Fatalf("system constraint lost or moved: %+v", compacted[0])
	}

	text := messagesToText(compacted)
	// (b) 最后一条 user 消息（当前任务）必须仍在。
	lastUser := original[len(original)-1].Content
	if !strings.Contains(text, lastUser) {
		t.Fatalf("last user message lost after compaction:\n%s", text)
	}
	// (d) 摘要必须作为消息注入。
	if !strings.Contains(text, "已确认结论：入口在 main.go") {
		t.Fatalf("summary content not injected:\n%s", text)
	}

	// (c) tool 配对安全：不允许孤立 tool 消息，也不允许 tool_call 没有结果。
	toolResults := map[string]bool{}
	openCalls := map[string]bool{}
	for _, message := range compacted {
		switch message.Role {
		case schema.Tool:
			if message.ToolCallID == "" {
				t.Fatalf("tool message without tool_call_id: %+v", message)
			}
			if !openCalls[message.ToolCallID] && !toolResults[message.ToolCallID] {
				t.Fatalf("orphan tool message for call %q", message.ToolCallID)
			}
			toolResults[message.ToolCallID] = true
		case schema.Assistant:
			for _, call := range message.ToolCalls {
				openCalls[call.ID] = true
			}
		}
	}
	for id := range openCalls {
		if !toolResults[id] {
			t.Fatalf("assistant tool_call %q has no matching tool result", id)
		}
	}
}

// TestEvalThresholdSensitivity 比较不同触发阈值下的行为与开销。
func TestEvalThresholdSensitivity(t *testing.T) {
	original := evalHistory(10, 8000)
	beforeTokens := estimateTokens(original)
	for _, threshold := range []int{500, 2000, 5000, 25000, 100000} {
		stub := &countingSummaryModel{reply: "已确认结论：入口在 main.go（证据：main.go:12）。"}
		middleware := evalMiddleware(t, config.ContextCompact{Enabled: true, TriggerTokens: threshold}, stub)
		compacted := runCompaction(t, middleware, original)
		afterTokens := estimateTokens(compacted)
		t.Logf("trigger_tokens=%d triggered=%v summary_calls=%d estimated_tokens %d->%d delta=%+d",
			threshold, stub.calls > 0, stub.calls, beforeTokens, afterTokens, afterTokens-beforeTokens)
	}
}

// TestEvalSummaryOverheadCrossover 找出「压缩反而让 prompt 变大」的临界规模。
// 摘要消息自带固定的前导说明与继续指令，历史比它短时压缩是净亏。
func TestEvalSummaryOverheadCrossover(t *testing.T) {
	crossover := -1
	for _, runesPerToolResult := range []int{100, 200, 400, 800, 1200, 2000, 4000} {
		original := evalHistory(1, runesPerToolResult)
		stub := &countingSummaryModel{reply: "已确认结论：入口在 main.go（证据：main.go:12）。"}
		middleware := evalMiddleware(t, config.ContextCompact{Enabled: true, TriggerTokens: 1}, stub)
		compacted := runCompaction(t, middleware, original)
		if stub.calls != 1 {
			t.Fatalf("threshold=1 should always trigger, got %d calls", stub.calls)
		}
		beforeTokens := estimateTokens(original)
		afterTokens := estimateTokens(compacted)
		t.Logf("runes_per_tool_result=%d estimated_tokens %d->%d delta=%+d",
			runesPerToolResult, beforeTokens, afterTokens, afterTokens-beforeTokens)
		if crossover < 0 && afterTokens < beforeTokens {
			crossover = runesPerToolResult
		}
	}
	if crossover < 0 {
		t.Fatal("every evaluated size was a net loss, or the sweep is too coarse")
	}
	t.Logf("crossover_runes_per_tool_result=%d (below this size, compaction is a net loss)", crossover)
}

// TestEvalCompactionOverhead 量化一次压缩的额外开销：摘要模型调用次数、
// 摘要提示的估算 token、以及中间件自身耗时（不含网络延迟）。
func TestEvalCompactionOverhead(t *testing.T) {
	original := evalHistory(8, 4000)
	stub := &countingSummaryModel{reply: "已确认结论：入口在 main.go（证据：main.go:12）。"}
	middleware := evalMiddleware(t, config.ContextCompact{Enabled: true, TriggerTokens: 1000}, stub)

	start := time.Now()
	compacted := runCompaction(t, middleware, original)
	elapsed := time.Since(start)

	if stub.calls != 1 {
		t.Fatalf("want exactly 1 summary model call, got %d", stub.calls)
	}
	promptTokens := estimateTokens(stub.inputs[0])
	promptMessages := len(stub.inputs[0])
	afterTokens := estimateTokens(compacted)
	t.Logf("summary_prompt_messages=%d summary_prompt_estimated_tokens=%d compacted_estimated_tokens=%d overhead_ratio_vs_result=%.2f middleware_cost=%s",
		promptMessages, promptTokens, afterTokens, float64(promptTokens)/float64(afterTokens), elapsed)
}

// TestEvalFinalizerBackfillsUserIntent 证明「用户意图回填」依赖模型按提示输出
// <all_user_messages> 块：输出缺失该块时，用户消息不会被回填。
// 这是在使用方 prompt 之外的隐含契约，值得在评估报告里显式记录。
func TestEvalFinalizerBackfillsUserIntent(t *testing.T) {
	original := evalHistory(2, 1000)
	lastUser := original[len(original)-1].Content

	withTag := &countingSummaryModel{reply: "结论：入口在 main.go。"}
	middleware := evalMiddleware(t, config.ContextCompact{Enabled: true, TriggerTokens: 1000}, withTag)
	compacted := runCompaction(t, middleware, original)
	if !strings.Contains(messagesToText(compacted), lastUser) {
		t.Fatal("user intent was not backfilled although the summary contained the tag block")
	}
}
