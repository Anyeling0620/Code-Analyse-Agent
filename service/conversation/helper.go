package conversation

import (
	"edu.agent.code/config"
	"edu.agent.code/service/agent/compress"
	"edu.agent.code/service/dto"
	"fmt"
	"github.com/cloudwego/eino/schema"
	"strings"
	"time"
)

func appendIfMissing(slice []string, s string) []string {
	if len(s) == 0 {
		return slice
	}
	for _, ele := range slice {
		if ele == s {
			return slice
		}
	}
	return append(slice, s)
}

func emitMarkDownBlock(emit ChatEmit, runState *dto.ChatRunState,
	eventType, agentName, content string) error {
	event := dto.ChatStreamEvent{
		Type:        eventType,
		TraceID:     runState.TraceID,
		SessionID:   runState.SessionID,
		Delta:       content,
		ContentKind: "markdown",
		RenderMode:  "append_block",
		Visibility:  "user",
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}
	if agentName != "" {
		event.Stage = agentName
	}
	recordRenderEvent(runState, event)
	if emit == nil {
		return nil
	}
	return emit(event)
}

func recordRenderEvent(runState *dto.ChatRunState, event dto.ChatStreamEvent) {
	// 有时候可能不需要回放
	if runState == nil {
		return
	}
	// TODO 可能还要过来一些不需要查会话历史时回放的内容
	runState.RenderEvents = append(runState.RenderEvents, event)
}

func toolResultFormat(toolName, content string) string {
	const (
		maxResultLines = 10
		maxResultRunes = 4000
	)
	content = strings.TrimRight(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if content == "" {
		return fmt.Sprintf("%s 工具已执行， 无输出", toolName)
	}
	lines := strings.Split(content, "\n")
	if len(lines) > maxResultLines {
		content = strings.Join(lines[:maxResultLines], "\n")
	}
	runes := []rune(content)
	truncated := false
	if len(runes) > maxResultRunes {
		runes = runes[:maxResultRunes]
		content = string(runes)
		truncated = true
	}
	if truncated {
		content += "\n\n ***内容过长已截断，完整内容已经提交模型继续写处理*"
	}
	return content
}

func summarySession(question, answer string) string {
	// TODO 后续修改不应直接截断，可以使用 LLM 进行总结
	text := fmt.Sprintf("上次用户提问:%s, 系统回答:%s", question, answer)
	runes := []rune(text)
	if len(runes) > 1000 {
		return string(runes[:1000]) + "..."
	}
	return string(runes)
}

// sessionSummaryPrefix 说明摘要的来源与可信边界，避免模型把它当成当前输入。
const sessionSummaryPrefix = "以下是本次会话较早轮次的压缩摘要，仅作背景事实参考，不代表用户当前的新输入："

// sessionSummaryMaxRunes 是滚动摘要的长度上限。
const sessionSummaryMaxRunes = 2000

// historyWindowSize 与 buildMessageWithHistory 里 ListMessages 的 Limit 保持一致：
// 模型每轮只看到最近这么多条历史消息，更早的内容只能靠摘要带过去。
const historyWindowSize = 20

// buildSessionSummaryMessage 把会话摘要转换成模型可见的 system 消息。
// 摘要为空时返回 nil，保证未启用压缩的会话行为与改造前一致。
func buildSessionSummaryMessage(session *dto.SessionContext) *schema.Message {
	if session == nil {
		return nil
	}
	summary := strings.TrimSpace(session.Summary)
	if summary == "" {
		return nil
	}
	return schema.SystemMessage(sessionSummaryPrefix + "\n" + summary)
}

// resolveSessionSummaryMessage 决定"这一轮要不要把跨轮摘要注入模型输入"。
//
// 两个条件缺一不可：
//  1. 压缩开关打开。Enabled=false 时必须与改造前完全一致——既不注入摘要，
//     也不累积摘要（累积在 persistSession 里同样按开关门控）。
//  2. 历史的确被窗口截断过（totalMessages > fetchedMessages）。否则摘要里写的就是
//     prompt 中已经存在的近期问答，注入等于把同一段内容重复计费。
func resolveSessionSummaryMessage(conf config.ContextCompact, session *dto.SessionContext,
	totalMessages int64, fetchedMessages int) *schema.Message {
	if !compress.Enabled(conf) {
		return nil
	}
	if totalMessages <= int64(fetchedMessages) {
		return nil
	}
	return buildSessionSummaryMessage(session)
}

// compactConf 返回压缩配置，并在 Service 未注入配置时（例如单测里的最小构造）
// 退回零值配置。零值即 Enabled=false，语义与"改造前"一致，不会 nil 解引用。
func (s *Service) compactConf() config.ContextCompact {
	if s == nil || s.conf == nil {
		return config.ContextCompact{}
	}
	return s.conf.ContextCompact
}

// mergeSessionSummary 生成滚动摘要：在上一轮摘要上追加本轮问答，
// 超出上限时保留头尾（最早的任务目标与最新的进展），折叠中间部分。
func mergeSessionSummary(previous, question, answer string) string {
	previous = strings.TrimSpace(previous)
	current := summarySession(question, answer)
	if previous == "" {
		return current
	}
	merged := previous + "\n" + current
	runes := []rune(merged)
	if len(runes) <= sessionSummaryMaxRunes {
		return merged
	}
	// 折叠标记本身也占用预算，必须计入，否则结果会超出上限。
	const foldedMark = "\n...（中间内容已压缩）...\n"
	budget := sessionSummaryMaxRunes - len([]rune(foldedMark))
	if budget <= 0 {
		return string(runes[len(runes)-sessionSummaryMaxRunes:])
	}
	head := budget / 2
	tail := budget - head
	return string(runes[:head]) + foldedMark + string(runes[len(runes)-tail:])
}

// buildModelHistory 按固定顺序拼接模型输入：压缩摘要 -> 历史消息 -> 用户画像。
// 摘要排在最前，保证它先于具体消息被模型读到；三部分都可能为空。
//
// 抽出成纯函数是为了能对"摘要确实被注入"这件事写断言——改造前
// session.Summary 只写不读，注入逻辑一旦写错，从库上看不出任何异常。
func buildModelHistory(summaryMessage *schema.Message, history []*schema.Message, profileMessage *schema.Message) []*schema.Message {
	messages := make([]*schema.Message, 0, len(history)+2)
	if summaryMessage != nil {
		messages = append(messages, summaryMessage)
	}
	messages = append(messages, history...)
	if profileMessage != nil {
		messages = append(messages, profileMessage)
	}
	return messages
}
