package conversation

import (
	"edu.agent.code/service/dto"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// runStateBrief 把一轮运行状态压成一行标量摘要，供日志使用。
//
// 不要直接把 runState 交给 zap.Any：它的 RenderEvents 会随轮次线性累积，
// 而 consumeAgentEvents 这条路径的日志是"每个事件一次"的高频调用，
// 每次都整份序列化会退化成 O(n²) 的日志放大（实测单轮可到 MB 级）。
//
// 这里只保留定位问题需要的标量：谁（run/session/trace）、产出多少
// （提问/回答的字符数）、事件条数、以及中断与降级两个状态位。
func runStateBrief(runState *dto.ChatRunState) zap.Field {
	if runState == nil {
		return zap.String("run_state", "nil")
	}
	return zap.String("run_state", fmt.Sprintf(
		"run_id=%s session_id=%s trace_id=%s question_runes=%d answer_runes=%d render_events=%d interrupted=%t degraded=%t",
		runState.RunID,
		runState.SessionID,
		runState.TraceID,
		len([]rune(runState.Question)),
		len([]rune(runState.Answer)),
		len(runState.RenderEvents),
		runState.Interrupted,
		runState.Degraded,
	))
}

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
