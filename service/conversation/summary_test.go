package conversation

import (
	"context"
	"strings"
	"testing"

	"edu.agent.code/adaptor/repo/session"
	"edu.agent.code/config"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"

	"github.com/cloudwego/eino/schema"
)

// fakePersistSessions 只实现写入路径，用来断言摘要在库里的写法。
type fakePersistSessions struct {
	session.ISession
	appended []*do.ChatMessageRecord
	upserted []*do.SessionContext
}

func (f *fakePersistSessions) AppendMessage(_ context.Context, record *do.ChatMessageRecord) error {
	f.appended = append(f.appended, record)
	return nil
}

func (f *fakePersistSessions) Upsert(_ context.Context, s *do.SessionContext) error {
	saved := *s
	f.upserted = append(f.upserted, &saved)
	return nil
}

func TestBuildSessionSummaryMessage(t *testing.T) {
	if message := buildSessionSummaryMessage(nil); message != nil {
		t.Fatalf("nil session should produce no summary message, got %+v", message)
	}
	if message := buildSessionSummaryMessage(&dto.SessionContext{Summary: "   \n "}); message != nil {
		t.Fatalf("blank summary should produce no summary message, got %+v", message)
	}

	message := buildSessionSummaryMessage(&dto.SessionContext{Summary: "上次用户提问:入口在哪, 系统回答:在 main.go"})
	if message == nil {
		t.Fatal("summary message is nil")
	}
	if message.Role != schema.System {
		t.Fatalf("summary role = %s, want system", message.Role)
	}
	if !strings.HasPrefix(message.Content, sessionSummaryPrefix) {
		t.Fatalf("summary should be prefixed by provenance note, got %q", message.Content)
	}
	if !strings.Contains(message.Content, "在 main.go") {
		t.Fatalf("summary content lost the original summary: %q", message.Content)
	}
}

func TestBuildModelHistoryOrdersSummaryFirst(t *testing.T) {
	summary := schema.SystemMessage("summary")
	history := []*schema.Message{schema.UserMessage("q1"), schema.AssistantMessage("a1", nil)}
	profile := schema.SystemMessage("profile")

	messages := buildModelHistory(summary, history, profile)
	if len(messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(messages))
	}
	if messages[0] != summary {
		t.Fatal("summary must be the first message so it is read before the raw history")
	}
	if messages[1] != history[0] || messages[2] != history[1] {
		t.Fatal("history order changed")
	}
	if messages[3] != profile {
		t.Fatal("profile message must stay at the end")
	}

	// 没有摘要、历史或画像时不应产生空消息占位。
	if got := buildModelHistory(nil, nil, nil); len(got) != 0 {
		t.Fatalf("empty input produced %d messages", len(got))
	}
	if got := buildModelHistory(nil, history, nil); len(got) != 2 {
		t.Fatalf("history-only input produced %d messages", len(got))
	}
}

func TestMergeSessionSummaryAccumulatesInsteadOfOverwriting(t *testing.T) {
	first := mergeSessionSummary("", "问题一", "回答一")
	if !strings.Contains(first, "问题一") {
		t.Fatalf("first summary lost the first turn: %q", first)
	}
	second := mergeSessionSummary(first, "问题二", "回答二")
	if !strings.Contains(second, "问题一") || !strings.Contains(second, "问题二") {
		t.Fatalf("summary was overwritten instead of accumulated: %q", second)
	}
}

func TestMergeSessionSummaryStaysWithinBudget(t *testing.T) {
	previous := strings.Repeat("旧", sessionSummaryMaxRunes)
	merged := mergeSessionSummary(previous, "问题", "回答")
	if runes := []rune(merged); len(runes) > sessionSummaryMaxRunes {
		t.Fatalf("merged summary length = %d runes, want <= %d", len(runes), sessionSummaryMaxRunes)
	}
	if !strings.Contains(merged, "中间内容已压缩") {
		t.Fatal("oversized summary should mark the folded middle section")
	}
	// 最新的问答必须保留，否则压缩会把当前任务丢掉。
	if !strings.Contains(merged, "问题") {
		t.Fatal("merged summary dropped the newest turn")
	}
}

// TestResolveSessionSummaryMessageRespectsCompactSwitch 锁定"开关说了算"：
// enabled=false 时无论历史是否被截断都不注入摘要，行为与改造前一致。
func TestResolveSessionSummaryMessageRespectsCompactSwitch(t *testing.T) {
	session := &dto.SessionContext{Summary: "早期轮次摘要"}
	enabled := config.ContextCompact{Enabled: true}
	disabled := config.ContextCompact{Enabled: false}

	if message := resolveSessionSummaryMessage(disabled, session, 100, historyWindowSize); message != nil {
		t.Fatalf("disabled compaction must not inject a summary, got %+v", message)
	}
	if message := resolveSessionSummaryMessage(enabled, session, 20, historyWindowSize); message != nil {
		t.Fatal("summary must not be injected while the whole history still fits the window")
	}

	message := resolveSessionSummaryMessage(enabled, session, 61, historyWindowSize)
	if message == nil {
		t.Fatal("summary should be injected once the history is longer than the window")
	}
	if message.Role != schema.System {
		t.Fatalf("summary role = %s, want system", message.Role)
	}
	if !strings.Contains(message.Content, "早期轮次摘要") {
		t.Fatalf("summary content lost: %q", message.Content)
	}
}

// TestPersistSessionSummaryFollowsCompactSwitch 断言摘要在库里的写法也跟随开关：
// 关闭时保持改造前的覆盖式写法，开启时才滚动累积。
func TestPersistSessionSummaryFollowsCompactSwitch(t *testing.T) {
	runState := &dto.ChatRunState{SessionID: "s1", Question: "本轮问题", Answer: "本轮回答"}
	sessions := &fakePersistSessions{}

	disabledService := &Service{sessions: sessions}
	disabledSession := &dto.SessionContext{SessionID: "s1", UserID: "u1", Summary: "更早的摘要"}
	if err := disabledService.persistSession(context.Background(), disabledSession, runState); err != nil {
		t.Fatalf("persistSession: %v", err)
	}
	if strings.Contains(disabledSession.Summary, "更早的摘要") {
		t.Fatalf("disabled compaction must keep the overwrite behaviour, got %q", disabledSession.Summary)
	}
	if !strings.Contains(disabledSession.Summary, "本轮问题") {
		t.Fatalf("disabled compaction must still store the last turn, got %q", disabledSession.Summary)
	}

	enabledService := &Service{
		sessions: sessions,
		conf:     &config.Config{ContextCompact: config.ContextCompact{Enabled: true}},
	}
	enabledSession := &dto.SessionContext{SessionID: "s1", UserID: "u1", Summary: "更早的摘要"}
	if err := enabledService.persistSession(context.Background(), enabledSession, runState); err != nil {
		t.Fatalf("persistSession: %v", err)
	}
	if !strings.Contains(enabledSession.Summary, "更早的摘要") || !strings.Contains(enabledSession.Summary, "本轮问题") {
		t.Fatalf("enabled compaction must accumulate the summary, got %q", enabledSession.Summary)
	}

	if len(sessions.upserted) != 2 {
		t.Fatalf("upserted rows = %d, want 2", len(sessions.upserted))
	}
}
