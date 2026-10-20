package conversation

import (
	"context"
	"strings"
	"testing"

	"edu.agent.code/service/agent/repo_analyzer"
	"edu.agent.code/service/consts"
	"edu.agent.code/service/dto"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// 回归：工具执行结果必须以 tool_result 事件下发。
// 若沿用 tool_call，前端会把它当成又一次工具调用，卡片永远停在“调用中”，
// 结果字段也不会渲染；同时该事件会落库用于历史回放，改名只在前端兜底会在刷新后丢结果。
func TestHandleToolCallResultEmitsToolResultEvent(t *testing.T) {
	svc := &Service{}
	runState := &dto.ChatRunState{ToolCallMap: map[string]dto.ToolCallState{}}
	msg := schema.ToolMessage("package main", "call-1", schema.WithToolName("read_files"))

	emitted := captureToolEvents(t, svc, msg, runState)

	if len(emitted) != 1 {
		t.Fatalf("expected exactly one emitted event, got %d", len(emitted))
	}
	got := emitted[0]
	if got.Type != consts.SseEventTypeToolResult {
		t.Errorf("emitted event type = %q, want %q", got.Type, consts.SseEventTypeToolResult)
	}
	if got.ToolName != "read_files" {
		t.Errorf("emitted tool name = %q, want %q", got.ToolName, "read_files")
	}
	if got.ToolResult == "" {
		t.Errorf("emitted tool result is empty, want the tool output")
	}
	if got.ToolCallID != "call-1" {
		t.Errorf("emitted tool call id = %q, want %q", got.ToolCallID, "call-1")
	}

	// 落库的事件类型必须与实时流一致，否则刷新后回放不出来。
	if len(runState.RenderEvents) != 1 {
		t.Fatalf("expected one persisted render event, got %d", len(runState.RenderEvents))
	}
	if persisted := runState.RenderEvents[0]; persisted.Type != consts.SseEventTypeToolResult {
		t.Errorf("persisted render event type = %q, want %q", persisted.Type, consts.SseEventTypeToolResult)
	}
	if len(runState.UsedTools) != 1 || runState.UsedTools[0] != "read_files" {
		t.Errorf("used tools = %v, want [read_files]", runState.UsedTools)
	}
}

// 工具结果消息没带 tool_name 时，名称要能回落到本轮记录的工具调用表。
func TestHandleToolCallResultFallsBackToToolCallMapName(t *testing.T) {
	svc := &Service{}
	runState := &dto.ChatRunState{ToolCallMap: map[string]dto.ToolCallState{
		"call-2": {Name: "repo_fetch", Arguments: `{"repo":"x"}`},
	}}
	msg := schema.ToolMessage("ok", "call-2")

	emitted := captureToolEvents(t, svc, msg, runState)

	if len(emitted) != 1 {
		t.Fatalf("expected exactly one emitted event, got %d", len(emitted))
	}
	if emitted[0].ToolName != "repo_fetch" {
		t.Errorf("emitted tool name = %q, want fallback %q", emitted[0].ToolName, "repo_fetch")
	}
}

// 回归：repo_analyzer 的二级子 Agent 只向 repo_analyzer 提供证据，
// 它们的 assistant 正文不能写进用户答案，否则一次仓库分析会出现多份“总结”。
func TestHandleMessageDropsInternalSubAgentText(t *testing.T) {
	svc := &Service{}
	runState := &dto.ChatRunState{ToolCallMap: map[string]dto.ToolCallState{}}
	var emitted []dto.ChatStreamEvent
	emit := func(event dto.ChatStreamEvent) error {
		emitted = append(emitted, event)
		return nil
	}

	msg := &schema.Message{Content: "子 Agent 的中间结论"}
	output := &adk.TypedMessageVariant[*schema.Message]{IsStreaming: true}
	event := &adk.AgentEvent{AgentName: "repo_callchain_analyzer"}

	if err := svc.handleMessage(context.Background(), msg, output, event, runState, emit); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}
	if runState.Answer != "" {
		t.Errorf("answer = %q, want empty (sub agent text must stay internal)", runState.Answer)
	}
	if len(emitted) != 0 {
		t.Fatalf("expected no emitted events, got %d", len(emitted))
	}
}

// repo_analyzer 自己的正文（阶段进展 + 最终报告）仍必须进入答案并实时下发。
func TestHandleMessageKeepsRepoAnalyzerText(t *testing.T) {
	svc := &Service{}
	runState := &dto.ChatRunState{ToolCallMap: map[string]dto.ToolCallState{}}
	var emitted []dto.ChatStreamEvent
	emit := func(event dto.ChatStreamEvent) error {
		emitted = append(emitted, event)
		return nil
	}

	msg := &schema.Message{Content: "## 一、项目概述"}
	output := &adk.TypedMessageVariant[*schema.Message]{IsStreaming: true}
	event := &adk.AgentEvent{AgentName: repo_analyzer.Name}

	if err := svc.handleMessage(context.Background(), msg, output, event, runState, emit); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}
	if runState.Answer != msg.Content {
		t.Errorf("answer = %q, want %q", runState.Answer, msg.Content)
	}
	if len(emitted) != 1 || emitted[0].Type != consts.SseEventTypeDelta || emitted[0].Stage != repo_analyzer.Name {
		t.Fatalf("emitted = %+v, want one delta staged %q", emitted, repo_analyzer.Name)
	}
}

// 过滤只针对正文：二级子 Agent 的工具调用仍要下发，用户才能看到分析进度。
func TestHandleMessageKeepsInternalSubAgentToolCalls(t *testing.T) {
	svc := &Service{}
	runState := &dto.ChatRunState{ToolCallMap: map[string]dto.ToolCallState{}}
	var emitted []dto.ChatStreamEvent
	emit := func(event dto.ChatStreamEvent) error {
		emitted = append(emitted, event)
		return nil
	}

	msg := &schema.Message{ToolCalls: []schema.ToolCall{{
		ID:       "call-1",
		Function: schema.FunctionCall{Name: "read_files", Arguments: `{"paths":["main.go"]}`},
	}}}
	output := &adk.TypedMessageVariant[*schema.Message]{IsStreaming: true}
	event := &adk.AgentEvent{AgentName: "repo_callchain_analyzer"}

	if err := svc.handleMessage(context.Background(), msg, output, event, runState, emit); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}
	if runState.ToolCalls != 1 {
		t.Errorf("tool calls = %d, want 1", runState.ToolCalls)
	}
	if len(emitted) != 1 || emitted[0].Type != consts.SseEventTypeToolCall || emitted[0].ToolName != "read_files" {
		t.Fatalf("emitted = %+v, want one tool_call for read_files", emitted)
	}
}

// 达到迭代上限且没有强制收尾产出时，走的是兜底文案路径：
// 已产出的正文必须保留（只追加提示），不能把半成品丢掉；
// 同时返回 nil，让上层照常发 done 并落库。
func TestHandleMaxIterationsExceededKeepsPartialAnswer(t *testing.T) {
	svc := &Service{}
	runState := &dto.ChatRunState{
		Answer:      "## 一、项目概述\n\n这是一个 Go 项目",
		ToolCallMap: map[string]dto.ToolCallState{},
	}
	var emitted []dto.ChatStreamEvent
	emit := func(event dto.ChatStreamEvent) error {
		emitted = append(emitted, event)
		return nil
	}
	event := &adk.AgentEvent{AgentName: repo_analyzer.Name, Err: adk.ErrExceedMaxIterations}

	if err := svc.handleMaxIterationsExceeded(context.Background(), event, runState, emit); err != nil {
		t.Fatalf("handleMaxIterationsExceeded returned error: %v", err)
	}
	if !strings.Contains(runState.Answer, "这是一个 Go 项目") {
		t.Fatalf("partial answer was dropped: %q", runState.Answer)
	}
	if !strings.Contains(runState.Answer, maxIterationsNoticeKey) {
		t.Fatalf("fallback notice missing: %q", runState.Answer)
	}
	if !runState.Degraded || runState.DegradedReason != dto.DegradedReasonMaxIterations {
		t.Fatalf("degraded=(%v,%q), want (true,%q)", runState.Degraded, runState.DegradedReason, dto.DegradedReasonMaxIterations)
	}
	if len(emitted) != 1 || emitted[0].Type != consts.SseEventTypeDelta {
		t.Fatalf("emitted = %+v, want one delta carrying the notice", emitted)
	}
	if !strings.Contains(emitted[0].Delta, maxIterationsNoticeKey) {
		t.Fatalf("emitted delta = %q, want the notice", emitted[0].Delta)
	}
}

// 子 Agent 超限会向上冒泡，父 Agent 可能再收到同类错误：兜底只能追加一次。
func TestHandleMaxIterationsExceededIsIdempotent(t *testing.T) {
	svc := &Service{}
	runState := &dto.ChatRunState{ToolCallMap: map[string]dto.ToolCallState{}}
	count := 0
	emit := func(dto.ChatStreamEvent) error {
		count++
		return nil
	}
	event := &adk.AgentEvent{Err: adk.ErrExceedMaxIterations}
	for i := 0; i < 3; i++ {
		if err := svc.handleMaxIterationsExceeded(context.Background(), event, runState, emit); err != nil {
			t.Fatalf("call %d returned error: %v", i, err)
		}
	}
	if got := strings.Count(runState.Answer, maxIterationsNoticeKey); got != 1 {
		t.Fatalf("notice appears %d times, want 1", got)
	}
	if count != 1 {
		t.Fatalf("emitted %d events, want 1", count)
	}
}

// 正文为空（模型一直在调工具，没产出任何结论）时，提示要引导用户缩小范围。
func TestHandleMaxIterationsExceededWithEmptyAnswer(t *testing.T) {
	svc := &Service{}
	runState := &dto.ChatRunState{ToolCallMap: map[string]dto.ToolCallState{}}

	if err := svc.handleMaxIterationsExceeded(context.Background(),
		&adk.AgentEvent{Err: adk.ErrExceedMaxIterations}, runState, nil); err != nil {
		t.Fatalf("handleMaxIterationsExceeded returned error: %v", err)
	}
	if !strings.Contains(runState.Answer, "请缩小问题范围后重试") {
		t.Fatalf("answer = %q, want the narrowing hint", runState.Answer)
	}
}

func captureToolEvents(t *testing.T, svc *Service, msg *schema.Message, runState *dto.ChatRunState) []dto.ChatStreamEvent {
	t.Helper()
	var emitted []dto.ChatStreamEvent
	emit := func(event dto.ChatStreamEvent) error {
		emitted = append(emitted, event)
		return nil
	}
	if err := svc.handleToolCallResult(msg, runState, emit, nil); err != nil {
		t.Fatalf("handleToolCallResult returned error: %v", err)
	}
	return emitted
}
