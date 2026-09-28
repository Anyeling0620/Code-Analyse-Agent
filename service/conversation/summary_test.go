package conversation

import (
	"strings"
	"testing"

	"edu.agent.code/service/dto"

	"github.com/cloudwego/eino/schema"
)

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
