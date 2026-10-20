package session

import (
	"context"
	"edu.agent.code/adaptor/repo/model"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func newSessionRepoTestDB(t *testing.T) (*gorm.DB, *Session) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "session_test.db")), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.ChatMessage{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db, NewSessionWithDB(db)
}

func mustMarshalEvents(t *testing.T, events []do.ChatStreamEvent) string {
	t.Helper()
	raw, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	return string(raw)
}

// seedMessages 建 5 条本会话消息 + 2 条干扰消息（其他用户/其他会话），
// 时间按插入顺序递增，分页应始终返回最新的一页。
func seedMessages(t *testing.T, db *gorm.DB) []uint {
	t.Helper()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []model.ChatMessage{
		{SessionID: "s1", UserID: "u1", Role: "user", Content: "q1", CreatedAt: base.Add(1 * time.Minute)},
		{SessionID: "s1", UserID: "u1", Role: "assistant", Content: "a1", CreatedAt: base.Add(2 * time.Minute),
			RenderEvents: mustMarshalEvents(t, []do.ChatStreamEvent{{Type: "tool_call", ToolName: "read_files"}})},
		{SessionID: "s1", UserID: "u1", Role: "user", Content: "q2", CreatedAt: base.Add(3 * time.Minute)},
		{SessionID: "s1", UserID: "u1", Role: "assistant", Content: "a2", CreatedAt: base.Add(4 * time.Minute),
			RenderEvents: mustMarshalEvents(t, []do.ChatStreamEvent{
				{Type: "tool_call", ToolName: "repo_analyzer"},
				{Type: "tool_result", ToolName: "repo_analyzer", ToolResult: "done"},
			})},
		{SessionID: "s1", UserID: "u1", Role: "user", Content: "q3", CreatedAt: base.Add(5 * time.Minute)},
		{SessionID: "s1", UserID: "u2", Role: "assistant", Content: "other user", CreatedAt: base.Add(6 * time.Minute)},
		{SessionID: "s2", UserID: "u1", Role: "assistant", Content: "other session", CreatedAt: base.Add(7 * time.Minute)},
	}
	ids := make([]uint, 0, len(rows))
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("create row %d: %v", i, err)
		}
		ids = append(ids, rows[i].ID)
	}
	return ids
}

func TestListMessagesPaginationWithoutCount(t *testing.T) {
	db, repo := newSessionRepoTestDB(t)
	ids := seedMessages(t, db)

	var captured []string
	if err := db.Callback().Query().After("gorm:query").Register("test:capture_sql", func(tx *gorm.DB) {
		captured = append(captured, tx.Statement.SQL.String())
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}

	page1, hasMore, err := repo.ListMessages(context.Background(), "u1", "s1", dto.Pager{Page: 1, Limit: 2}, false)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if !hasMore {
		t.Fatal("page1 should report hasMore")
	}
	if len(page1) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(page1))
	}
	// 最新一页是 [a2, q3]，页内按时间升序。
	if page1[0].ID != ids[3] || page1[1].ID != ids[4] {
		t.Fatalf("page1 ids = [%d %d], want [%d %d]", page1[0].ID, page1[1].ID, ids[3], ids[4])
	}
	if page1[0].RenderEvents != nil || page1[1].RenderEvents != nil {
		t.Fatal("lightweight page must not unmarshal render events")
	}
	if !page1[0].HasRenderEvents {
		t.Fatal("assistant message with events must report HasRenderEvents")
	}
	if !page1[0].HasProjectAnalysis {
		t.Fatal("assistant message calling repo_analyzer must report HasProjectAnalysis")
	}
	if page1[1].HasRenderEvents || page1[1].HasProjectAnalysis {
		t.Fatal("user message must not report events/project analysis")
	}

	// 该页只有 2 条，limit+1 会多取一条触发 hasMore；不得出现 COUNT(*)。
	for _, sql := range captured {
		if strings.Contains(strings.ToUpper(sql), "COUNT(") {
			t.Fatalf("ListMessages must not COUNT(*), got: %s", sql)
		}
	}

	page2, hasMore, err := repo.ListMessages(context.Background(), "u1", "s1", dto.Pager{Page: 2, Limit: 2}, false)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if !hasMore || len(page2) != 2 || page2[0].ID != ids[1] || page2[1].ID != ids[2] {
		t.Fatalf("page2 = %+v hasMore=%v, want ids [%d %d] and hasMore", page2, hasMore, ids[1], ids[2])
	}

	page3, hasMore, err := repo.ListMessages(context.Background(), "u1", "s1", dto.Pager{Page: 3, Limit: 2}, false)
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if hasMore || len(page3) != 1 || page3[0].ID != ids[0] {
		t.Fatalf("page3 = %+v hasMore=%v, want only id %d", page3, hasMore, ids[0])
	}

	// include_render_events=true 保持旧行为：事件被反序列化并返回。
	full, _, err := repo.ListMessages(context.Background(), "u1", "s1", dto.Pager{Page: 1, Limit: 2}, true)
	if err != nil {
		t.Fatalf("full page: %v", err)
	}
	if len(full) != 2 || len(full[0].RenderEvents) != 2 {
		t.Fatalf("full page events = %+v, want 2 events on first row", full)
	}
	if full[0].RenderEvents[0].ToolName != "repo_analyzer" {
		t.Fatalf("full page tool name = %q", full[0].RenderEvents[0].ToolName)
	}
}

func TestListMessagesUsesCompositeIndex(t *testing.T) {
	db, _ := newSessionRepoTestDB(t)
	var count int64
	if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_chat_messages_user_session_created'").Scan(&count).Error; err != nil {
		t.Fatalf("query index: %v", err)
	}
	if count != 1 {
		t.Fatalf("composite index count = %d, want 1", count)
	}
}

func TestGetMessageChecksOwnership(t *testing.T) {
	db, repo := newSessionRepoTestDB(t)
	ids := seedMessages(t, db)

	got, err := repo.GetMessage(context.Background(), "u1", "s1", ids[3])
	if err != nil {
		t.Fatalf("GetMessage own: %v", err)
	}
	if got == nil || len(got.RenderEvents) != 2 || got.RenderEvents[0].ToolName != "repo_analyzer" {
		t.Fatalf("GetMessage own = %+v", got)
	}
	if !got.HasRenderEvents || !got.HasProjectAnalysis {
		t.Fatalf("GetMessage flags = hasEvents:%v hasProject:%v", got.HasRenderEvents, got.HasProjectAnalysis)
	}

	for _, tc := range []struct {
		name      string
		userID    string
		sessionID string
		messageID uint
	}{
		{name: "other user", userID: "u2", sessionID: "s1", messageID: ids[3]},
		{name: "other session", userID: "u1", sessionID: "s2", messageID: ids[3]},
		{name: "missing", userID: "u1", sessionID: "s1", messageID: 99999},
	} {
		record, err := repo.GetMessage(context.Background(), tc.userID, tc.sessionID, tc.messageID)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if record != nil {
			t.Fatalf("%s: got %+v, want nil", tc.name, record)
		}
	}
}
