package conversation

import (
	"context"
	"edu.agent.code/adaptor/repo/session"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeHistorySessions 覆盖 GetSessionInfo/GetMessage 用到的读取路径，
// 其余方法沿用内嵌 nil 接口（调用会 panic，正好暴露越界依赖）。
type fakeHistorySessions struct {
	session.ISession
	sessionRow  *do.SessionContext
	messages    []do.ChatMessageRecord
	hasMore     bool
	gotInclude  bool
	listCalls   int
	messageByID map[uint]*do.ChatMessageRecord
	gotUserID   string
	gotSession  string
}

func (f *fakeHistorySessions) GetByID(_ context.Context, _ string) (*do.SessionContext, error) {
	return f.sessionRow, nil
}

func (f *fakeHistorySessions) ListMessages(_ context.Context, userID, sessionID string, _ dto.Pager, includeRenderEvents bool) ([]do.ChatMessageRecord, bool, error) {
	f.listCalls++
	f.gotUserID = userID
	f.gotSession = sessionID
	f.gotInclude = includeRenderEvents
	return f.messages, f.hasMore, nil
}

func (f *fakeHistorySessions) GetMessage(_ context.Context, userID, sessionID string, messageID uint) (*do.ChatMessageRecord, error) {
	f.gotUserID = userID
	f.gotSession = sessionID
	return f.messageByID[messageID], nil
}

func TestGetSessionInfoDefaultsToLightweight(t *testing.T) {
	sessions := &fakeHistorySessions{
		sessionRow: &do.SessionContext{SessionID: "s1", UserID: "u1"},
		messages: []do.ChatMessageRecord{
			{ID: 1, SessionID: "s1", UserID: "u1", Role: "user", Content: "q"},
			{ID: 2, SessionID: "s1", UserID: "u1", Role: "assistant", Content: "a", HasRenderEvents: true, HasProjectAnalysis: true},
		},
		hasMore: true,
	}
	svc := &Service{sessions: sessions}

	resp, err := svc.GetSessionInfo(context.Background(), "u1", &dto.GetSessionInfo{
		SessionID: "s1",
		Pager:     dto.Pager{Page: 1, Limit: 8},
	})
	if err != nil {
		t.Fatalf("GetSessionInfo: %v", err)
	}
	if sessions.gotInclude {
		t.Fatal("default request must not ask the repo for render_events")
	}
	if !resp.HasMore || len(resp.List) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
	for _, msg := range resp.List {
		// copier 会把 nil 切片复制成空切片；JSON 侧 omitempty 仍会省略该字段。
		if len(msg.RenderEvents) != 0 {
			t.Fatalf("lightweight response leaked render events: %+v", msg)
		}
	}
	if !resp.List[1].HasRenderEvents || !resp.List[1].HasProjectAnalysis {
		t.Fatalf("flags not copied: %+v", resp.List[1])
	}
	if resp.Total != 0 {
		t.Fatalf("total = %d, want 0 (COUNT removed)", resp.Total)
	}

	// 线格式层面确认轻量响应不含 render_events，但仍带 has_render_events。
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal resp: %v", err)
	}
	if strings.Contains(string(body), `"render_events":`) {
		t.Fatalf("lightweight JSON leaked render_events: %s", body)
	}
	if !strings.Contains(string(body), `"has_render_events":true`) {
		t.Fatalf("lightweight JSON missing has_render_events: %s", body)
	}

	// 显式 include_render_events=1 时透传给仓储，保持旧调用方兼容。
	if _, err := svc.GetSessionInfo(context.Background(), "u1", &dto.GetSessionInfo{
		SessionID:           "s1",
		IncludeRenderEvents: true,
		Pager:               dto.Pager{Page: 1, Limit: 8},
	}); err != nil {
		t.Fatalf("GetSessionInfo full: %v", err)
	}
	if !sessions.gotInclude {
		t.Fatal("include_render_events must be forwarded to the repo")
	}
}

func TestGetSessionInfoRejectsForeignSession(t *testing.T) {
	sessions := &fakeHistorySessions{
		sessionRow: &do.SessionContext{SessionID: "s1", UserID: "owner"},
	}
	svc := &Service{sessions: sessions}

	resp, err := svc.GetSessionInfo(context.Background(), "visitor", &dto.GetSessionInfo{
		SessionID: "s1",
		Pager:     dto.Pager{Page: 1, Limit: 8},
	})
	if err != nil {
		t.Fatalf("GetSessionInfo: %v", err)
	}
	if resp != nil {
		t.Fatalf("foreign session must return nil resp, got %+v", resp)
	}
	if sessions.listCalls != 0 {
		t.Fatal("foreign session must not read messages")
	}
}

func TestGetMessageNotFoundAndOwnershipFilter(t *testing.T) {
	sessions := &fakeHistorySessions{messageByID: map[uint]*do.ChatMessageRecord{
		7: {ID: 7, SessionID: "s1", UserID: "u1", Role: "assistant", RenderEvents: []do.ChatStreamEvent{{Type: "tool_call", ToolName: "read_files"}}},
	}}
	svc := &Service{sessions: sessions}

	got, err := svc.GetMessage(context.Background(), "u1", &dto.GetMessage{SessionID: "s1", MessageID: 7})
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got == nil || len(got.RenderEvents) != 1 {
		t.Fatalf("GetMessage = %+v", got)
	}
	if sessions.gotUserID != "u1" || sessions.gotSession != "s1" {
		t.Fatalf("ownership filter not passed: user=%s session=%s", sessions.gotUserID, sessions.gotSession)
	}

	_, err = svc.GetMessage(context.Background(), "u1", &dto.GetMessage{SessionID: "s1", MessageID: 404})
	if !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("missing message err = %v, want ErrMessageNotFound", err)
	}
}

func TestGetMessageCompactsByDefaultAndRawKeepsAll(t *testing.T) {
	sessions := &fakeHistorySessions{messageByID: map[uint]*do.ChatMessageRecord{
		8: {ID: 8, SessionID: "s1", UserID: "u1", Role: "assistant", HasRenderEvents: true, RenderEvents: []do.ChatStreamEvent{
			{Type: "reason", Delta: "thinking"},
			{Type: "delta", Delta: "先说明。", Visibility: "user", RenderMode: "append_block"},
			{Type: "delta", Delta: "再说明。", Visibility: "user", RenderMode: "append_block"},
			{Type: "progress", Delta: "", Visibility: "user", RenderMode: "append_block"},
			{Type: "tool_call", ToolName: "", ToolArguments: "partial"},
			{Type: "tool_call", ToolName: "read_files", ToolCallID: "c1", Visibility: "user", RenderMode: "append_tool"},
			{Type: "tool_result", ToolName: "read_files", ToolCallID: "c1", ToolResult: "ok", Visibility: "user", RenderMode: "append_tool"},
			{Type: "done"},
		}},
	}}
	svc := &Service{sessions: sessions}

	got, err := svc.GetMessage(context.Background(), "u1", &dto.GetMessage{SessionID: "s1", MessageID: 8})
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	var types []string
	for _, event := range got.RenderEvents {
		types = append(types, event.Type)
	}
	wantTypes := []string{"delta", "tool_call", "tool_result"}
	if len(types) != len(wantTypes) {
		t.Fatalf("compacted types = %v, want %v", types, wantTypes)
	}
	for i := range wantTypes {
		if types[i] != wantTypes[i] {
			t.Fatalf("compacted types = %v, want %v", types, wantTypes)
		}
	}
	if got.RenderEvents[0].Delta != "先说明。再说明。" {
		t.Fatalf("consecutive deltas not merged: %q", got.RenderEvents[0].Delta)
	}
	if !got.HasRenderEvents {
		t.Fatal("compacted message with renderable events must keep HasRenderEvents")
	}

	raw, err := svc.GetMessage(context.Background(), "u1", &dto.GetMessage{SessionID: "s1", MessageID: 8, Raw: true})
	if err != nil {
		t.Fatalf("GetMessage raw: %v", err)
	}
	if len(raw.RenderEvents) != 8 {
		t.Fatalf("raw events = %d, want 8", len(raw.RenderEvents))
	}
}

func TestCompactRenderEventsDropsUnrenderableAndKeepsOrder(t *testing.T) {
	events := []do.ChatStreamEvent{
		{Type: "progress", Message: "阶段一", Visibility: "user", RenderMode: "append_block"},
		{Type: "delta", Delta: "a", Visibility: "user", RenderMode: "append_block"},
		{Type: "reason", Delta: "hidden"},
		{Type: "delta", Delta: "b", Visibility: "user", RenderMode: "append_block"},
		{Type: "delta", Delta: "hidden", Visibility: "internal", RenderMode: "append_block"},
		{Type: "tool_call", ToolName: "read_files", ToolCallID: "c1"},
		{Type: "delta", Delta: "c", Visibility: "user", RenderMode: "append_block"},
		{Type: "error", Message: ""},
		{Type: "error", Message: "boom"},
		{Type: "observe", Stage: "tool_call", Detail: "x"},
		{Type: "observe"},
	}
	got := compactRenderEvents(events)
	var types []string
	for _, event := range got {
		types = append(types, event.Type)
	}
	want := []string{"progress", "delta", "tool_call", "delta", "error", "observe"}
	if len(types) != len(want) {
		t.Fatalf("types = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("types = %v, want %v", types, want)
		}
	}
	// 中间隔着被丢掉的事件时，delta 仍可合并；遇到 tool_call 后必须断开。
	if got[1].Delta != "ab" || got[3].Delta != "c" {
		t.Fatalf("delta merge wrong: %q %q", got[1].Delta, got[3].Delta)
	}
	if got[0].Message != "阶段一" {
		t.Fatalf("progress content lost: %+v", got[0])
	}

	empty := compactRenderEvents([]do.ChatStreamEvent{{Type: "reason", Delta: "only reasoning"}})
	if len(empty) != 0 {
		t.Fatalf("all-unrenderable events should compact to empty, got %+v", empty)
	}
}
