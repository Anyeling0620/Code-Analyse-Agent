package conversation

import (
	"fmt"
	"strings"
	"testing"

	"edu.agent.code/service/dto"

	"github.com/cloudwego/eino/schema"
)

// 本文件评估跨轮（非单次 ReAct 循环）这一半的压缩成效：滚动摘要是否被注入、
// 是否有长度上限、以及它把跨轮 prompt 压到什么量级。
//
// token 口径与 compress 包一致：约 4 runes/token，仅用于相对比较。

func evalEstimateTokens(messages []*schema.Message) int {
	var builder strings.Builder
	for _, message := range messages {
		if message == nil {
			continue
		}
		builder.WriteString(message.Content)
		builder.WriteString("\n")
	}
	runes := len([]rune(builder.String()))
	if runes == 0 {
		return 0
	}
	return runes/4 + 1
}

// evalLongTurns 模拟一个已经被压缩前的跨轮历史：每轮都带一条很大的工具结果。
func evalLongTurns(turns, runesPerTurn int) []*schema.Message {
	messages := make([]*schema.Message, 0, turns*3+2)
	messages = append(messages, schema.SystemMessage("系统约束：只读分析，禁止破坏性操作"))
	repeat := runesPerTurn / 26
	if repeat < 1 {
		repeat = 1
	}
	for i := 0; i < turns; i++ {
		messages = append(messages,
			schema.UserMessage(fmt.Sprintf("第 %d 轮：请分析模块 %d", i+1, i+1)),
			schema.AssistantMessage(strings.Repeat("分析过程说明：调用工具读取源码。\n", repeat/4+1), nil),
			schema.ToolMessage(strings.Repeat("文件正文片段：func Handler() { /* 实现细节 */ }\n", repeat), fmt.Sprintf("call-%d", i)),
		)
	}
	return messages
}

// TestEvalCrossTurnPromptIsCapped 对比「原始历史」与「滚动摘要 + 最近若干轮」的 prompt 体积。
func TestEvalCrossTurnPromptIsCapped(t *testing.T) {
	history := evalLongTurns(20, 4000)
	rawPrompt := buildModelHistory(nil, history, nil)
	rawTokens := evalEstimateTokens(rawPrompt)

	// 压缩后的输入形状：一条有上限的会话摘要 + 最近 4 条消息。
	summary := buildSessionSummaryMessage(&dto.SessionContext{
		Summary: strings.Repeat("已确认事实与证据位置。", 150), // 600 runes，低于上限
	})
	if summary == nil {
		t.Fatal("summary message should not be nil when Summary is set")
	}
	recent := history[len(history)-4:]
	compactedPrompt := buildModelHistory(summary, recent, nil)
	compactedTokens := evalEstimateTokens(compactedPrompt)

	t.Logf("raw_history_messages=%d raw_estimated_tokens=%d", len(rawPrompt), rawTokens)
	t.Logf("rolling_summary_messages=%d rolling_estimated_tokens=%d reduction=%.1f%%",
		len(compactedPrompt), compactedTokens,
		100*float64(rawTokens-compactedTokens)/float64(rawTokens))

	if compactedTokens >= rawTokens {
		t.Fatalf("rolling summary did not reduce cross-turn prompt: %d -> %d", rawTokens, compactedTokens)
	}
	// 摘要必须真的排在历史之前，否则模型先读到的是细节而不是任务状态。
	if compactedPrompt[0] != summary {
		t.Fatal("summary message is not the first message in model history")
	}
}

// TestEvalRollingSummaryStaysBounded 验证滚动摘要不会无限增长，且保留最早目标与最新进展。
func TestEvalRollingSummaryStaysBounded(t *testing.T) {
	summary := ""
	first := "第一个目标：定位项目入口与启动链路，证据要求给出文件与行号。"
	for i := 0; i < 40; i++ {
		question := fmt.Sprintf("第 %d 轮问题：继续分析模块 %d 的调用链", i+1, i+1)
		answer := strings.Repeat(fmt.Sprintf("第 %d 轮结论：证据在 module-%d.go:42。", i+1, i+1), 8)
		if i == 0 {
			question = first
		}
		summary = mergeSessionSummary(summary, question, answer)
		runes := len([]rune(summary))
		if runes > sessionSummaryMaxRunes {
			t.Fatalf("rolling summary exceeded cap at turn %d: %d runes > %d", i+1, runes, sessionSummaryMaxRunes)
		}
	}
	t.Logf("rolling_summary_runes=%d cap=%d", len([]rune(summary)), sessionSummaryMaxRunes)
	if !strings.Contains(summary, "第一个目标") {
		t.Fatalf("earliest task goal was dropped from rolling summary: %q", summary)
	}
	if !strings.Contains(summary, "第 40 轮") {
		t.Fatalf("latest progress was dropped from rolling summary: %q", summary)
	}
}

// TestEvalModelHistoryOrderAndNilSafety 验证注入顺序与空值安全（未启用压缩时行为不变）。
func TestEvalModelHistoryOrderAndNilSafety(t *testing.T) {
	history := []*schema.Message{schema.UserMessage("当前问题")}
	summary := buildSessionSummaryMessage(&dto.SessionContext{Summary: "历史摘要"})
	profile := schema.SystemMessage("用户画像：偏好 Go")

	got := buildModelHistory(summary, history, profile)
	if len(got) != 3 || got[0] != summary || got[1] != history[0] || got[2] != profile {
		t.Fatalf("unexpected order: %+v", got)
	}
	if nilSummary := buildSessionSummaryMessage(&dto.SessionContext{}); nilSummary != nil {
		t.Fatalf("empty session summary should yield nil message, got %+v", nilSummary)
	}
	if buildSessionSummaryMessage(nil) != nil {
		t.Fatal("nil session should yield nil message")
	}
	if onlyHistory := buildModelHistory(nil, history, nil); len(onlyHistory) != 1 || onlyHistory[0] != history[0] {
		t.Fatalf("disabled-compaction shape changed: %+v", onlyHistory)
	}
}
