package conversation

import (
	"edu.agent.code/service/dto"
	"fmt"
	"strings"
	"time"
)

func appendIfMissing(slice []string, s string) []string {
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
