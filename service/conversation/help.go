package conversation

import (
	"edu.agent.code/service/dto"
	"time"
)

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
