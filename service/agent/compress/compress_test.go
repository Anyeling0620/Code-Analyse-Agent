package compress

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"edu.agent.code/config"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// fakeSummaryModel 只实现摘要需要的 Generate；Stream 用于证明不会被调用。
type fakeSummaryModel struct {
	summary string
}

func (f *fakeSummaryModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage(f.summary, nil), nil
}

func (f *fakeSummaryModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream is not supported by fakeSummaryModel")
}

// recordingModel 记录每次模型调用收到的消息，用来证明压缩发生在"真正调用模型之前"。
type recordingModel struct {
	inputs [][]*schema.Message
}

func (r *recordingModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	r.inputs = append(r.inputs, input)
	return schema.AssistantMessage("已确认结论：入口是 main.go。", nil), nil
}

func (r *recordingModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream is not supported by recordingModel")
}

func (r *recordingModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return r, nil
}

func TestBuildPolicyFallsBackToDefaults(t *testing.T) {
	policy := BuildPolicy(config.ContextCompact{})
	if policy.WindowTokens != DefaultWindowTokens {
		t.Fatalf("window tokens = %d, want %d", policy.WindowTokens, DefaultWindowTokens)
	}
	want := int(float64(DefaultWindowTokens) * DefaultTriggerRatio)
	if policy.TriggerTokens != want {
		t.Fatalf("trigger tokens = %d, want %d", policy.TriggerTokens, want)
	}
	if policy.TriggerMessages != DefaultTriggerMessages {
		t.Fatalf("trigger messages = %d, want %d", policy.TriggerMessages, DefaultTriggerMessages)
	}
}

func TestBuildPolicyHonoursExplicitValues(t *testing.T) {
	policy := BuildPolicy(config.ContextCompact{
		WindowTokens:    64000,
		TriggerRatio:    0.8,
		TriggerTokens:   12345,
		TriggerMessages: 7,
	})
	// 显式 TriggerTokens 优先于按比例推导。
	if policy.TriggerTokens != 12345 {
		t.Fatalf("trigger tokens = %d, want 12345", policy.TriggerTokens)
	}
	if policy.TriggerMessages != 7 {
		t.Fatalf("trigger messages = %d, want 7", policy.TriggerMessages)
	}

	// 只给窗口和比例时按比例推导。
	policy = BuildPolicy(config.ContextCompact{WindowTokens: 64000, TriggerRatio: 0.8})
	if policy.TriggerTokens != 51200 {
		t.Fatalf("trigger tokens = %d, want 51200", policy.TriggerTokens)
	}

	// 非法比例回落到默认比例。
	policy = BuildPolicy(config.ContextCompact{WindowTokens: 1000, TriggerRatio: 3})
	if policy.TriggerTokens != int(1000*DefaultTriggerRatio) {
		t.Fatalf("trigger tokens = %d, want %d", policy.TriggerTokens, int(1000*DefaultTriggerRatio))
	}
}

func TestNewReturnsNilWhenDisabled(t *testing.T) {
	middleware, err := New(context.Background(), config.ContextCompact{Enabled: false}, &fakeSummaryModel{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if middleware != nil {
		t.Fatalf("middleware = %v, want nil when disabled", middleware)
	}
	handlers, err := NewHandlers(context.Background(), config.ContextCompact{Enabled: false}, &fakeSummaryModel{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(handlers) != 0 {
		t.Fatalf("handlers = %d, want 0 when disabled", len(handlers))
	}
}

func TestNewRequiresModelWhenEnabled(t *testing.T) {
	if _, err := New(context.Background(), config.ContextCompact{Enabled: true}, nil); err == nil {
		t.Fatal("expected error when compaction is enabled without a model")
	}
}

// TestSummarizeFoldsHistoryAndKeepsPinnedContext 断言压缩后的历史满足两个硬约束：
//  1. system 消息（安全约束、画像等）原样保留；
//  2. 不再出现孤立的 tool 消息 —— 工具结果被折叠进摘要，而不是留下配不上
//     assistant tool_call 的悬空 ToolMessage（那种消息会让下一次模型调用直接 400）。
func TestSummarizeFoldsHistoryAndKeepsPinnedContext(t *testing.T) {
	ctx := context.Background()
	middleware, err := New(ctx, config.ContextCompact{Enabled: true, TriggerMessages: 1}, &fakeSummaryModel{
		summary: "关键结论：项目入口是 main.go。\n<all_user_messages>\n</all_user_messages>",
	})
	if err != nil {
		t.Fatalf("build middleware: %v", err)
	}
	concrete, ok := middleware.(*summarization.TypedMiddleware[*schema.Message])
	if !ok {
		t.Fatalf("middleware type = %T, want *summarization.TypedMiddleware[*schema.Message]", middleware)
	}

	state := &adk.ChatModelAgentState{Messages: []*schema.Message{
		schema.SystemMessage("安全约束：不得执行破坏性命令"),
		schema.UserMessage("第一个问题：项目入口在哪"),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:       "call-1",
			Type:     "function",
			Function: schema.FunctionCall{Name: "read_files", Arguments: "{}"},
		}}),
		schema.ToolMessage("很长的工具输出：...", "call-1"),
		schema.UserMessage("第二个问题：配置放在哪"),
	}}

	summarized, err := concrete.Summarize(ctx, state)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if len(summarized) == 0 {
		t.Fatal("summarized history is empty")
	}
	if summarized[0].Role != schema.System || summarized[0].Content != "安全约束：不得执行破坏性命令" {
		t.Fatalf("system message was not preserved: role=%s content=%q", summarized[0].Role, summarized[0].Content)
	}
	for _, message := range summarized {
		if message.Role == schema.Tool {
			t.Fatalf("orphan tool message survived compaction: %+v", message)
		}
	}
	summaryText := summarized[len(summarized)-1].Content
	if !strings.Contains(summaryText, "关键结论：项目入口是 main.go。") {
		t.Fatalf("summary content missing model output: %q", summaryText)
	}
	// 用户意图必须被回填进摘要，否则压缩等于丢掉任务目标。
	for _, want := range []string{"第一个问题：项目入口在哪", "第二个问题：配置放在哪"} {
		if !strings.Contains(summaryText, want) {
			t.Fatalf("summary missing user intent %q: %q", want, summaryText)
		}
	}
}

// TestCompactionFiresBeforeModelCall 是端到端验证：把压缩中间件挂到真实 agent 上，
// 用超阈值的历史跑一次 Run，断言模型实际收到的消息已经被压缩过。
//
// 这条断言很关键——单元测试只证明中间件本身能压缩，只有跑一次 Run 才能证明它
// 挂在了"每次模型调用之前"这个位置上（换句话说，Handlers 接线没接错）。
func TestCompactionFiresBeforeModelCall(t *testing.T) {
	ctx := context.Background()
	modelRecorder := &recordingModel{}
	middleware, err := New(ctx, config.ContextCompact{Enabled: true, TriggerMessages: 4}, modelRecorder)
	if err != nil {
		t.Fatalf("build middleware: %v", err)
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "compact-e2e",
		Description:   "compaction end to end probe",
		Model:         modelRecorder,
		Handlers:      []adk.ChatModelAgentMiddleware{middleware},
		MaxIterations: 3,
	})
	if err != nil {
		t.Fatalf("build agent: %v", err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})

	// 构造一条贴近真实负载的历史：多轮 ReAct 循环，每轮都带回一大块工具结果
	// （read_files 单次上限 5000 runes、项目检索单次约 8000 runes）。
	history := syntheticToolHeavyHistory(6, 8000)
	iter := runner.Run(ctx, history)
	for event, ok := iter.Next(); ok; event, ok = iter.Next() {
		if event.Err != nil {
			t.Fatalf("agent run failed: %v", event.Err)
		}
	}

	if len(modelRecorder.inputs) < 2 {
		t.Fatalf("model called %d times, want >= 2 (one summarization call + one agent call)", len(modelRecorder.inputs))
	}
	agentInput := modelRecorder.inputs[len(modelRecorder.inputs)-1]
	if len(agentInput) >= len(history) {
		t.Fatalf("compaction did not fire: model saw %d messages, history had %d", len(agentInput), len(history))
	}
	joined := messagesToText(agentInput)
	if !strings.Contains(joined, "已确认结论：入口是 main.go。") {
		t.Fatalf("summary was not injected into the model input: %s", joined)
	}
	if !strings.Contains(joined, "系统约束：只读分析") {
		t.Fatal("system constraint was dropped by compaction")
	}

	// 成效量化：同一条历史，压缩前后估算 token 的对比。
	// 估算口径与 Eino 默认计数器一致：无 usage 时按约 4 runes/token。
	before := estimateTokens(history)
	after := estimateTokens(agentInput)
	t.Logf("compaction effect: messages %d -> %d, estimated tokens %d -> %d (%.1f%% smaller)",
		len(history), len(agentInput), before, after, 100*float64(before-after)/float64(before))
	if after >= before {
		t.Fatalf("compaction did not reduce estimated tokens: before=%d after=%d", before, after)
	}
}

// syntheticToolHeavyHistory 生成 rounds 轮"用户提问 -> 模型调工具 -> 一大块工具结果"
// 的历史，用来量化压缩效果。首条是 system 约束，最后一条是当前用户问题。
func syntheticToolHeavyHistory(rounds, runesPerToolResult int) []*schema.Message {
	messages := []*schema.Message{schema.SystemMessage("系统约束：只读分析，不得执行破坏性命令")}
	for i := 0; i < rounds; i++ {
		messages = append(messages,
			schema.UserMessage(fmt.Sprintf("第 %d 轮问题：请分析这个模块", i+1)),
			schema.AssistantMessage("", []schema.ToolCall{{
				ID:       fmt.Sprintf("call-%d", i),
				Type:     "function",
				Function: schema.FunctionCall{Name: "read_files", Arguments: "{}"},
			}}),
			schema.ToolMessage(strings.Repeat("工具输出片段", runesPerToolResult/6), fmt.Sprintf("call-%d", i)),
		)
	}
	return append(messages, schema.UserMessage("当前问题：把上面的结论汇总成报告"))
}

func messagesToText(messages []*schema.Message) string {
	var builder strings.Builder
	for _, message := range messages {
		if message == nil {
			continue
		}
		builder.WriteString(message.Content)
		builder.WriteString("\n")
	}
	return builder.String()
}

// estimateTokens 是压缩成效估算口径：约 4 个 rune 记 1 个 token。
// 它只用于相对比较（压缩前 vs 压缩后），不代表真实分词结果。
func estimateTokens(messages []*schema.Message) int {
	runes := len([]rune(messagesToText(messages)))
	if runes == 0 {
		return 0
	}
	return runes/4 + 1
}
