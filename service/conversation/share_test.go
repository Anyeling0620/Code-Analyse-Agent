package conversation

import (
	"context"
	"edu.agent.code/adaptor/repo/session"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"
	"encoding/json"
	"testing"
)

type fakeShares struct {
	session.IShare
	record *do.SessionShare
}

func (f *fakeShares) GetByToken(_ context.Context, _ string) (*do.SessionShare, error) {
	return f.record, nil
}

func TestGetSharedSessionCompactsEventsByDefault(t *testing.T) {
	snapshot := dto.ShareSnapshot{
		Session: &dto.SessionContext{SessionID: "s1", UserID: "u1"},
		Total:   1,
		List: []*dto.ChatMessageRecord{{
			ID: 1, SessionID: "s1", UserID: "u1", Role: "assistant", Content: "最终回答",
			RenderEvents: []dto.ChatStreamEvent{
				{Type: "reason", Delta: "thinking"},
				{Type: "delta", Delta: "先说明。", Visibility: "user", RenderMode: "append_block"},
				{Type: "delta", Delta: "再说明。", Visibility: "user", RenderMode: "append_block"},
				{Type: "tool_call", ToolArguments: "fragment"},
				{Type: "tool_call", ToolName: "repo_analyzer", ToolCallID: "c1", Visibility: "user", RenderMode: "append_tool"},
			},
		}},
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	svc := &Service{shares: &fakeShares{record: &do.SessionShare{ShareToken: "t", Payload: string(payload)}}}

	light, err := svc.GetSharedSession(context.Background(), "t", false)
	if err != nil {
		t.Fatalf("GetSharedSession: %v", err)
	}
	events := light.List[0].RenderEvents
	if len(events) != 2 {
		t.Fatalf("compacted events = %d, want 2: %+v", len(events), events)
	}
	if events[0].Type != "delta" || events[0].Delta != "先说明。再说明。" {
		t.Fatalf("delta not merged: %+v", events[0])
	}
	if events[1].Type != "tool_call" || events[1].ToolName != "repo_analyzer" {
		t.Fatalf("tool call lost: %+v", events[1])
	}
	if !light.List[0].HasRenderEvents || !light.List[0].HasProjectAnalysis {
		t.Fatalf("flags = hasEvents:%v hasProject:%v", light.List[0].HasRenderEvents, light.List[0].HasProjectAnalysis)
	}

	raw, err := svc.GetSharedSession(context.Background(), "t", true)
	if err != nil {
		t.Fatalf("GetSharedSession raw: %v", err)
	}
	if len(raw.List[0].RenderEvents) != 5 {
		t.Fatalf("raw events = %d, want 5", len(raw.List[0].RenderEvents))
	}
}
