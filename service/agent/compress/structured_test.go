package compress

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"edu.agent.code/config"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// structured_test.go 锁定默认策略（StrategyStructured）的契约：
//   - system 消息永不进入摘要模型输入，且压缩后逐字保留；
//   - 最近 DefaultKeepRecent 条消息原样保留，不经过摘要模型；
//   - 只有更早的历史会被折叠成一条结构化摘要；
//   - 没有可压缩内容或摘要为空时，宁可原样返回也不丢消息。
//
// 同时用一条对照用例锁定"新旧策略在摘要输入上确实不同"：旧策略把**全部非 system
// 上下文**（含最近的工具结果）都投给摘要模型，新策略只投更早的一段。

// blankSummaryModel 返回空内容，用来验证"摘要为空就放弃压缩"的兜底。
type blankSummaryModel struct{}

func (b *blankSummaryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("", nil), nil
}

func (b *blankSummaryModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream is not supported by blankSummaryModel")
}

func contents(messages []*schema.Message) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		out = append(out, message.Content)
	}
	return out
}

func TestSegmentSplitsSystemOlderRecent(t *testing.T) {
	messages := []*schema.Message{
		schema.SystemMessage("约束一"),
		schema.UserMessage("u1"),
		schema.AssistantMessage("a1", nil),
		schema.UserMessage("u2"),
		schema.AssistantMessage("a2", nil),
		schema.UserMessage("u3"),
	}
	segmented := Segment(messages, 2)

	if len(segmented.System) != 1 || segmented.System[0].Content != "约束一" {
		t.Fatalf("system = %d 条, want 1 条(约束一)", len(segmented.System))
	}
	// 5 条非 system 消息，keepRecent=2 → 最近 2 条留在 Recent，其余 3 条进 Older。
	if got := contents(segmented.Recent); len(got) != 2 || got[0] != "a2" || got[1] != "u3" {
		t.Fatalf("recent = %v, want [a2 u3]", got)
	}
	if got := contents(segmented.Older); len(got) != 3 || got[0] != "u1" || got[2] != "u2" {
		t.Fatalf("older = %v, want [u1 a1 u2]", got)
	}
	for _, message := range append(append([]*schema.Message{}, segmented.Older...), segmented.Recent...) {
		if message.Role == schema.System {
			t.Fatalf("system 消息漏进了可压缩区: %q", message.Content)
		}
	}
}

func TestSegmentKeepsSystemOutOfRecentCount(t *testing.T) {
	messages := []*schema.Message{
		schema.SystemMessage("s1"),
		schema.SystemMessage("s2"),
		schema.UserMessage("u1"),
		schema.UserMessage("u2"),
	}
	// keepRecent=2：system 不参与计数，所以最近两条非 system 消息都在 Recent，Older 为空。
	segmented := Segment(messages, 2)
	if len(segmented.System) != 2 {
		t.Fatalf("system = %d 条, want 2（system 不该把 recent 的额度挤掉）", len(segmented.System))
	}
	if len(segmented.Older) != 0 {
		t.Fatalf("older = %v, want 空", contents(segmented.Older))
	}
	if len(segmented.Recent) != 2 {
		t.Fatalf("recent = %d 条, want 2", len(segmented.Recent))
	}
}

func TestSegmentExpandsToolPairBoundary(t *testing.T) {
	// 切点正好落在一条 tool 结果上时，必须向前扩展到对应的 assistant 工具调用，
	// 否则会留下"配不上 tool_call 的悬空 ToolMessage"。
	messages := []*schema.Message{
		schema.SystemMessage("约束"),
		schema.UserMessage("u1"),
		schema.AssistantMessage("a1", nil),
		schema.UserMessage("u2"),
		schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Type: "function",
			Function: schema.FunctionCall{Name: "read_files", Arguments: "{}"}}}),
		schema.ToolMessage("工具结果", "call-1"),
		schema.UserMessage("u3"),
		schema.AssistantMessage("a3", nil),
	}
	// 非 system 消息共 7 条；keepRecent=3 时朴素切点正好落在 tool 结果上（index 4），
	// 必须向前扩展到 index 3 的 assistant 工具调用，所以 Recent 变成 4 条。
	segmented := Segment(messages, 3)
	if len(segmented.Recent) == 0 || segmented.Recent[0].Role == schema.Tool {
		t.Fatalf("recent 首条是悬空 tool 结果: %v", contents(segmented.Recent))
	}
	if len(segmented.Recent) != 4 {
		t.Fatalf("recent = %v, want 4 条（assistant 工具调用 + tool 结果 + u3 + a3）", contents(segmented.Recent))
	}
	if len(segmented.Recent[0].ToolCalls) == 0 || segmented.Recent[0].ToolCalls[0].ID != "call-1" {
		t.Fatalf("recent 首条不是对应的 assistant 工具调用: %+v", segmented.Recent[0])
	}
}

func TestSegmentFallsBackToDefaultKeepRecent(t *testing.T) {
	for _, keepRecent := range []int{0, -3} {
		messages := []*schema.Message{schema.SystemMessage("约束")}
		for i := 0; i < DefaultKeepRecent+2; i++ {
			messages = append(messages, schema.UserMessage(fmt.Sprintf("u%d", i)))
		}
		segmented := Segment(messages, keepRecent)
		if len(segmented.Recent) != DefaultKeepRecent {
			t.Fatalf("keepRecent=%d: recent = %d 条, want %d", keepRecent, len(segmented.Recent), DefaultKeepRecent)
		}
		if len(segmented.Older) != 2 {
			t.Fatalf("keepRecent=%d: older = %d 条, want 2", keepRecent, len(segmented.Older))
		}
	}
}

func TestResolveStrategyDefaultsToStructured(t *testing.T) {
	cases := map[string]string{
		"":                     StrategyStructured,
		"structured":           StrategyStructured,
		"legacy":               StrategyLegacy,
		"  unknown-strategy  ": StrategyStructured,
		"LEGACY":               StrategyStructured, // 大小写不敏感不做支持，避免配置写错就静默回滚
	}
	for raw, want := range cases {
		if got := ResolveStrategy(config.ContextCompact{Strategy: raw}); got != want {
			t.Fatalf("ResolveStrategy(%q) = %q, want %q", raw, got, want)
		}
	}
	if got := ResolveKeepRecent(config.ContextCompact{}); got != DefaultKeepRecent {
		t.Fatalf("ResolveKeepRecent(0) = %d, want %d", got, DefaultKeepRecent)
	}
	if got := ResolveKeepRecent(config.ContextCompact{KeepRecent: 3}); got != 3 {
		t.Fatalf("ResolveKeepRecent(3) = %d, want 3", got)
	}
}

// TestStructuredSummaryInputExcludesSystemAndRecent 是本改造最核心的一条断言：
// 摘要模型看不到 system 提示词，也看不到最近原文。
func TestStructuredSummaryInputExcludesSystemAndRecent(t *testing.T) {
	history := structuredHistory(6)
	stub := &countingSummaryModel{reply: "【任务目标】继续分析。"}
	middleware := evalMiddleware(t, config.ContextCompact{Enabled: true, TriggerTokens: 1}, stub)

	compacted := runCompaction(t, middleware, history)
	if stub.calls != 1 {
		t.Fatalf("summary 调用次数 = %d, want 1", stub.calls)
	}
	input := stub.inputs[0]
	for _, message := range input {
		if message.Role == schema.System && strings.Contains(message.Content, "系统约束") {
			t.Fatalf("业务 system 消息进了摘要输入: %q", message.Content)
		}
	}
	segmented := Segment(history, DefaultKeepRecent)
	if len(input) != len(segmented.Older)+2 {
		t.Fatalf("摘要输入 = %d 条, want 1(sysInstruction)+%d(older)+1(userInstruction)", len(input), len(segmented.Older))
	}
	joined := messagesToText(input)
	// 最近几轮的模块编号只应出现在 Recent 里，不该出现在摘要输入里。
	for _, round := range []int{4, 5, 6} {
		if strings.Contains(joined, fmt.Sprintf("模块%d", round)) {
			t.Fatalf("第 %d 轮（最近原文）被塞进了摘要输入：等于回到旧策略", round)
		}
	}
	if !strings.Contains(joined, segmented.Older[0].Content) {
		t.Fatal("较早的历史没有进摘要输入")
	}
	if len(compacted) != len(segmented.System)+1+len(segmented.Recent) {
		t.Fatalf("压缩后 = %d 条, want system(%d)+1+recent(%d)",
			len(compacted), len(segmented.System), len(segmented.Recent))
	}
}

func TestStructuredFinalizeRebuildsSystemSummaryRecent(t *testing.T) {
	history := structuredHistory(6)
	segmented := Segment(history, DefaultKeepRecent)
	stub := &countingSummaryModel{reply: "【已确认事实】入口是 main.go —— main.go:1。"}
	middleware := evalMiddleware(t, config.ContextCompact{Enabled: true, TriggerTokens: 1}, stub)

	compacted := runCompaction(t, middleware, history)
	if len(compacted) == 0 {
		t.Fatal("压缩后历史为空")
	}
	// system 消息逐字保留且仍在最前。
	if len(segmented.System) != 1 {
		t.Fatalf("system 段 = %d 条, want 1", len(segmented.System))
	}
	if compacted[0] != segmented.System[0] {
		t.Fatalf("system 消息被替换了: got %q want %q", compacted[0].Content, segmented.System[0].Content)
	}
	// 第二条是摘要，带"内部摘要"标记，避免后续被当成用户新输入。
	summaryMessage := compacted[1]
	if summaryMessage.Role != schema.User {
		t.Fatalf("摘要消息 role = %s, want user", summaryMessage.Role)
	}
	if got := summaryMessage.Extra[summaryExtraKey]; got != summaryExtraValue {
		t.Fatalf("摘要消息缺少 content_type 标记: %v", summaryMessage.Extra)
	}
	if !strings.Contains(summaryMessage.Content, "【已确认事实】") {
		t.Fatalf("摘要内容未被写入: %q", summaryMessage.Content)
	}
	// 其余是最近原文，逐条指针相同（= 没有被改写）。
	recent := compacted[2:]
	if len(recent) != len(segmented.Recent) {
		t.Fatalf("最近原文 = %d 条, want %d", len(recent), len(segmented.Recent))
	}
	for i := range recent {
		if recent[i] != segmented.Recent[i] {
			t.Fatalf("第 %d 条最近消息不是原文: got %q want %q", i, recent[i].Content, segmented.Recent[i].Content)
		}
	}
}

func TestStructuredFinalizeFallsBackWhenNothingToCompress(t *testing.T) {
	// 非 system 消息不超过 keepRecent 时，Older 为空：必须原样返回，绝不丢消息。
	history := []*schema.Message{
		schema.SystemMessage("约束"),
		schema.UserMessage("u1"),
		schema.AssistantMessage("a1", nil),
	}
	stub := &countingSummaryModel{reply: "【任务目标】不该被用上。"}
	middleware := evalMiddleware(t, config.ContextCompact{Enabled: true, TriggerMessages: 1}, stub)

	compacted := runCompaction(t, middleware, history)
	if len(compacted) != len(history) {
		t.Fatalf("无可压缩内容时消息数变了: %d -> %d", len(history), len(compacted))
	}
	for i := range history {
		if compacted[i] != history[i] {
			t.Fatalf("第 %d 条消息被改动: %q", i, compacted[i].Content)
		}
	}
}

func TestStructuredFinalizeFallsBackOnEmptySummary(t *testing.T) {
	history := structuredHistory(4)
	middleware := evalMiddleware(t, config.ContextCompact{Enabled: true, TriggerTokens: 1}, &blankSummaryModel{})

	compacted := runCompaction(t, middleware, history)
	if len(compacted) != len(history) {
		t.Fatalf("摘要为空时必须原样返回: %d -> %d", len(history), len(compacted))
	}
	for i := range history {
		if compacted[i] != history[i] {
			t.Fatalf("第 %d 条消息被改动: %q", i, compacted[i].Content)
		}
	}
}

// TestStrategyDifferenceOnSummaryInput 对照锁定"两种策略在摘要输入上确实不同"。
//
// 事实口径（读 Eino v0.9.21 源码确认）：默认路径会把**全部非 system 上下文**投给摘要模型，
// 所以旧策略下最近的工具结果也会进摘要；新策略只投更早的一段，最近原文不进摘要。
func TestStrategyDifferenceOnSummaryInput(t *testing.T) {
	history := structuredHistory(6)
	newestMarker := "模块6" // 属于最近 DefaultKeepRecent 条

	legacyStub := &countingSummaryModel{reply: "旧策略摘要"}
	legacyMiddleware := evalMiddleware(t,
		config.ContextCompact{Enabled: true, TriggerTokens: 1, Strategy: StrategyLegacy}, legacyStub)
	runCompaction(t, legacyMiddleware, history)
	if legacyStub.calls != 1 {
		t.Fatalf("旧策略 summary 调用次数 = %d, want 1", legacyStub.calls)
	}
	if !strings.Contains(messagesToText(legacyStub.inputs[0]), newestMarker) {
		t.Fatalf("旧策略应当把最近原文也投给摘要模型（含 %s）", newestMarker)
	}

	structuredStub := &countingSummaryModel{reply: "新策略摘要"}
	structuredMiddleware := evalMiddleware(t,
		config.ContextCompact{Enabled: true, TriggerTokens: 1, Strategy: StrategyStructured}, structuredStub)
	runCompaction(t, structuredMiddleware, history)
	if structuredStub.calls != 1 {
		t.Fatalf("新策略 summary 调用次数 = %d, want 1", structuredStub.calls)
	}
	if strings.Contains(messagesToText(structuredStub.inputs[0]), newestMarker) {
		t.Fatalf("新策略不该把最近原文投给摘要模型（%s 不该出现）", newestMarker)
	}
}

// TestDefaultStructuredInstructionPinsEvidenceRules 锁定默认摘要提示词的两条硬约束，
// 避免后续被改成"自由发挥"式摘要。
func TestDefaultStructuredInstructionPinsEvidenceRules(t *testing.T) {
	for _, want := range []string{
		"【任务目标】", "【已确认事实】", "【已做决策】",
		"【未解决问题与下一步】", "【失败尝试及原因】", "【用户历史约束】",
		"未确认", "不得把推断写成事实", "不得把本摘要任务的要求",
	} {
		if !strings.Contains(structuredDefaultInstruction, want) {
			t.Fatalf("默认结构化摘要指令缺少 %q", want)
		}
	}
}

// structuredHistory 构造 1 条 system + rounds 轮"用户提问 -> 工具调用 -> 工具结果" + 当前问题。
// 每轮带唯一模块编号，便于断言"哪一轮的内容出现在哪里"。
func structuredHistory(rounds int) []*schema.Message {
	messages := []*schema.Message{schema.SystemMessage("系统约束：只读分析，结论必须给出文件路径与行号")}
	for i := 0; i < rounds; i++ {
		round := i + 1
		callID := fmt.Sprintf("call-%d", i)
		messages = append(messages,
			schema.UserMessage(fmt.Sprintf("第 %d 轮：分析模块%d", round, round)),
			schema.AssistantMessage("", []schema.ToolCall{{ID: callID, Type: "function",
				Function: schema.FunctionCall{Name: "read_files", Arguments: fmt.Sprintf("{\"file\":\"module-%d.go\"}", round)}}}),
			schema.ToolMessage(fmt.Sprintf("模块%d的工具输出片段：文件正文如下 %s", round,
				strings.Repeat("实现细节 ", 30)), callID),
		)
	}
	return append(messages, schema.UserMessage("当前问题：基于上面的分析给出结论与证据"))
}
