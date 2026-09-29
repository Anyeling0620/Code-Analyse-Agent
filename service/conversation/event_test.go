package conversation

import (
	"testing"

	"edu.agent.code/service/consts"
	"edu.agent.code/service/dto"

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
