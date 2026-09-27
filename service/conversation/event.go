package conversation

import (
	"context"
	"edu.agent.code/service/consts"
	"edu.agent.code/service/dto"
	"edu.agent.code/service/tool/terminal"
	"edu.agent.code/utils/logger"
	"errors"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	"io"
	"time"
)

func (s *Service) consumeAgentEvents(
	ctx context.Context,
	iter *adk.AsyncIterator[*adk.AgentEvent],
	runState *dto.ChatRunState,
	emit ChatEmit) error {
	for event, ok := iter.Next(); ok; event, ok = iter.Next() {
		if event.Err != nil {
			logger.Error("consumeAgentEvents error", zap.Any("event", event), zap.Any("runState", runState), zap.Error(event.Err))
			if errors.Is(event.Err, context.Canceled) {
				// TODO 达到最大迭代次数， 这里应该处理强制输出报告
			}
			if emit != nil {
				err := emitMarkDownBlock(emit, runState, consts.SseEventTypeError, event.AgentName, event.Err.Error())
				if err != nil {
					logger.Error("consumeAgentEvents emitMarkdownBlock error", zap.Any("event", event), zap.Any("runState", runState), zap.Error(err))
					return err
				}
			}
			return event.Err
		}
		logger.Info("consumeAgentEvents start", zap.Any("event", iter), zap.Any("runState", runState))
		if event.Action != nil && event.Action.Interrupted != nil {
			// TODO 这里发生了中断 需要进入中断处理 恢复可以返回
			return s.handleInterruptedEvent(ctx, event, runState, emit)
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		output := event.Output.MessageOutput
		if output.IsStreaming && output.MessageStream != nil {
			// 处理流式内容
			err := s.consumeMessageStream(ctx, output, event, runState, emit)
			if err != nil {
				logger.Error("consumeAgentEvents consumeMessageStream error", zap.Any("event", event), zap.Any("runState", runState), zap.Error(err))
				err = emitMarkDownBlock(emit, runState, consts.SseEventTypeError, event.AgentName, err.Error())
				if err != nil {
					logger.Error("consumeAgentEvents emitMarkDownBlock error", zap.Any("event", event), zap.Any("runState", runState), zap.Error(err))
				}
				return err
			}
			continue
		}
		// 如果不是流式
		msg, err := output.GetMessage()
		if err != nil {
			logger.Error("consumeAgentEvents getMessage error", zap.Any("event", event), zap.Any("runState", runState), zap.Error(err))
			return err
		}
		// 处理消息
		err = s.handleMessage(ctx, msg, output, event, runState, emit)
	}
	return nil
}

func (s *Service) consumeMessageStream(
	ctx context.Context,
	output *adk.TypedMessageVariant[*schema.Message],
	event *adk.AgentEvent,
	runState *dto.ChatRunState,
	emit ChatEmit,
) error {
	if output.IsStreaming && output.MessageStream != nil {
		// 处理流式内容
		stream := output.MessageStream
		defer stream.Close()

		fullMsg := ""
		for {
			msg, err := stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					logger.Info("consumeMessageStream EOF",
						zap.Any("event", event),
						zap.Any("runState", runState),
						zap.Any("message", fullMsg))
					break
				}
				return err
			}
			fullMsg = fullMsg + msg.Content
			err = s.handleMessage(ctx, msg, output, event, runState, emit)
			if err != nil {
				logger.Error("consumeMessageStream handleMessage")
				return err
			}

		}
		return nil
	}

	return nil
}

func (s *Service) handleMessage(
	ctx context.Context,
	msg *schema.Message,
	output *adk.TypedMessageVariant[*schema.Message],
	event *adk.AgentEvent,
	runState *dto.ChatRunState,
	emit ChatEmit,
) error {
	if msg == nil {
		return nil
	}
	// TODO 记录 token 消耗
	err := s.trackUsage(ctx, runState, msg, event.AgentName)
	if err != nil {
		logger.Error("handleMessage trackUsage error", zap.Any("runState", runState), zap.Any("err", err))
		// 不要中断
	}
	if len(msg.ToolCalls) > 0 {
		return s.handleToolCall(emit, runState, event, msg)
	}
	// 工具调用
	if msg.Role == schema.Tool {
		return s.handleToolCallResult(msg, runState, emit, event)
	}
	// 如果只是流式输出 跳过
	isAssistantText := msg.Role == schema.Assistant || (output.IsStreaming && msg.Role == "")
	if !isAssistantText {
		return nil
	}
	if len(msg.Content) == 0 && len(msg.ReasoningContent) == 0 {
		return nil
	}
	if output.IsStreaming {
		runState.Answer = runState.Answer + msg.Content
		runState.ReasoningContent = runState.ReasoningContent + msg.ReasoningContent
		eventType := consts.SseEventTypeDelta
		content := msg.Content
		// 正在推理
		if len(msg.Content) == 0 && len(msg.ReasoningContent) != 0 {
			eventType = consts.SseEventTypeReason
			content = msg.ReasoningContent
		}
		return emitMarkDownBlock(emit, runState, eventType, event.AgentName, content)
	}
	// TODO 如果不是流式呢？疑惑
	return emitMarkDownBlock(emit, runState, consts.SseEventTypeProgress, event.AgentName, msg.Content)
}

func (s *Service) handleToolCallResult(msg *schema.Message, runState *dto.ChatRunState, emit ChatEmit, event *adk.AgentEvent) error {
	toolName := msg.ToolName
	if toolName == "" && msg.ToolCallID != "" {
		toolName = runState.ToolCallMap[msg.ToolCallID].Name
	}
	runState.UsedTools = appendIfMissing(runState.UsedTools, toolName)
	emitEvent := dto.ChatStreamEvent{
		Type:        consts.SseEventTypeToolCall,
		TraceID:     runState.TraceID,
		SessionID:   runState.SessionID,
		ToolName:    toolName,
		ToolCallID:  msg.ToolCallID,
		ToolResult:  toolResultFormat(toolName, msg.Content),
		ContentKind: "tool",
		RenderMode:  "append_tool",
		Visibility:  "user",
		Timestamp:   time.Now().UTC().Format(time.DateTime),
	}

	recordRenderEvent(runState, emitEvent)

	if emit != nil {
		if err := emit(emitEvent); err != nil {
			logger.Error("handleMessage emitEvent error", zap.Any("event", event), zap.Any("runState", runState), zap.Error(err))
			return err
		}
	}
	return nil
}

func (s *Service) handleToolCall(emit ChatEmit, runState *dto.ChatRunState, event *adk.AgentEvent, msg *schema.Message) error {
	err := emitMarkDownBlock(
		emit,
		runState,
		consts.SseEventTypeProgress,
		event.AgentName,
		msg.Content)
	if err != nil {
		logger.Error("handleToolCall error", zap.Any("event", event), zap.Any("runState", runState), zap.Error(err))

		return err
	}

	// 调用工具
	for _, call := range msg.ToolCalls {
		toolName := call.Function.Name
		runState.UsedTools = appendIfMissing(runState.UsedTools, toolName)
		if call.ID != "" {
			runState.ToolCallMap[call.ID] = dto.ToolCallState{
				Name:      toolName,
				Arguments: call.Function.Arguments,
			}
		}
		emitEvent := dto.ChatStreamEvent{
			Type:          consts.SseEventTypeToolCall,
			TraceID:       runState.TraceID,
			SessionID:     runState.SessionID,
			ToolName:      toolName,
			ToolCallID:    call.ID,
			ToolArguments: call.Function.Arguments,
			ContentKind:   "tool",
			RenderMode:    "append_tool",
			Visibility:    "user",
			Timestamp:     time.Now().UTC().Format(time.DateTime),
		}

		recordRenderEvent(runState, emitEvent)

		if emit != nil {
			if err := emit(emitEvent); err != nil {
				logger.Error("handleToolCall emitEvent error", zap.Any("event", event), zap.Any("runState", runState), zap.Error(err))
				return err
			}
		}

	}
	return nil
}

func (s *Service) handleInterruptedEvent(
	ctx context.Context,
	event *adk.AgentEvent,
	runState *dto.ChatRunState,
	emit ChatEmit) error {
	if event == nil || event.Action == nil || event.Action.Interrupted == nil {
		return nil
	}

	infoFromInterruptFunc := func(interruptInfo any) (terminal.ApprovalInfo, bool) {
		switch info := interruptInfo.(type) {
		case terminal.ApprovalInfo:
			return info, true
		default:
			return terminal.ApprovalInfo{}, false
		}
	}

	for _, interruptCtx := range event.Action.Interrupted.InterruptContexts {
		if interruptCtx == nil || !interruptCtx.IsRootCause {
			continue
		}
		info, ok := infoFromInterruptFunc(interruptCtx)
		if !ok {
			continue
		}
		if s.approvals != nil {
			err := s.approvals.BindInterrupt(ctx, info.PendingID, info.CheckpointID, interruptCtx.ID)
			if err != nil {
				logger.Error("handleInterrupt error", zap.Any("event", event), zap.Any("runState", runState), zap.Any("info", info), zap.Error(err))
				return err
			}
		}
		runState.Interrupted = true
		runState.PendingApprovalID = info.PendingID
		if info.Tool != "" {
			runState.UsedTools = appendIfMissing(runState.UsedTools, info.Tool)
		}
		interruptEvent := dto.ChatStreamEvent{
			Type:              consts.SseEventTypeInterrupt,
			TraceID:           runState.TraceID,
			SessionID:         runState.SessionID,
			ToolName:          info.Tool,
			ToolCallID:        info.ToolCallID,
			Message:           "检测到需要用户确认的操作",
			PendingApprovalID: info.PendingID,
			PendingCommand:    info.Command,
			PendingRiskReason: info.Reason,
			PendingRiskLevel:  info.RiskLevel,
			PendingWorkDir:    info.Workdir,
			PendingTimeoutSec: info.TimeoutSec,
		}
		recordRenderEvent(runState, interruptEvent)
		if emit != nil {
			err := emit(interruptEvent)
			if err != nil {
				logger.Error("handleInterruptedEvent interruptedEvent emit error", zap.Any("event", event), zap.Any("runState", runState), zap.Error(err))
				return err
			}
		}
		pauseEvent := dto.ChatStreamEvent{
			Type:        consts.SseEventTypeToolResult,
			TraceID:     runState.TraceID,
			SessionID:   runState.SessionID,
			ToolName:    info.Tool,
			ToolCallID:  info.ToolCallID,
			ContentKind: "tool",
			RenderMode:  "append_tool",
			Visibility:  "user",
			Timestamp:   time.Now().Format(time.DateTime),
			ToolResult:  "命令需要用户确认审批，已暂停执行",
		}
		recordRenderEvent(runState, pauseEvent)
		if emit != nil {
			err := emit(pauseEvent)
			if err != nil {
				logger.Error("handleInterruptedEvent pauseEvent emit error", zap.Any("event", event), zap.Any("runState", runState), zap.Error(err))
				return err
			}
		}
		return nil
	}
	return nil
}
