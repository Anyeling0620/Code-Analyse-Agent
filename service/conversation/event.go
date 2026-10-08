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
	"strings"
	"time"
)

func (s *Service) consumeAgentEvents(
	ctx context.Context,
	iter *adk.AsyncIterator[*adk.AgentEvent],
	runState *dto.ChatRunState,
	emit ChatEmit) error {
	for event, ok := iter.Next(); ok; event, ok = iter.Next() {
		if event.Err != nil {
			logger.Error("consumeAgentEvents error", zap.Any("event", event), runStateBrief(runState), zap.Error(event.Err))
			// 达到最大迭代次数不是"执行失败"：此时模型往往已经产出了大半结论，
			// 直接按错误返回会把这份半成品丢掉，用户只看到一个错误气泡。
			// 这里改为强制收尾，把已有内容当部分报告下发，让上层照常走 done 与落库。
			// 注意：判定必须用 adk.ErrExceedMaxIterations；early 版本这里误写成
			// context.Canceled，而超限走的根本不是取消路径，永远不会命中。
			if errors.Is(event.Err, adk.ErrExceedMaxIterations) {
				return s.handleMaxIterationsExceeded(ctx, event, runState, emit)
			}
			if emit != nil {
				err := emitMarkDownBlock(emit, runState, consts.SseEventTypeError, event.AgentName, event.Err.Error())
				if err != nil {
					logger.Error("consumeAgentEvents emitMarkdownBlock error", zap.Any("event", event), runStateBrief(runState), zap.Error(err))
					return err
				}
			}
			return event.Err
		}
		interruptedAction := event.Action != nil && event.Action.Interrupted != nil
		// 原来这里打的是迭代器本身（zap.Any("event", iter)），既没有信息量又每事件一次；
		// 换成 agent + 是否中断动作这两个标量。
		logger.Info("consumeAgentEvents start",
			zap.String("agent", event.AgentName),
			zap.Bool("interrupted_action", interruptedAction),
			runStateBrief(runState))
		if interruptedAction {
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
				logger.Error("consumeAgentEvents consumeMessageStream error", zap.Any("event", event), runStateBrief(runState), zap.Error(err))
				err = emitMarkDownBlock(emit, runState, consts.SseEventTypeError, event.AgentName, err.Error())
				if err != nil {
					logger.Error("consumeAgentEvents emitMarkDownBlock error", zap.Any("event", event), runStateBrief(runState), zap.Error(err))
				}
				return err
			}
			continue
		}
		// 如果不是流式
		msg, err := output.GetMessage()
		if err != nil {
			logger.Error("consumeAgentEvents getMessage error", zap.Any("event", event), runStateBrief(runState), zap.Error(err))
			return err
		}
		// 处理消息
		err = s.handleMessage(ctx, msg, output, event, runState, emit)
	}
	return nil
}

// maxIterationsNoticeKey 是兜底文案的标志句，用来做"只追加一次"的幂等判定。
const maxIterationsNoticeKey = "已达到本轮最大工具调用轮次"

// maxIterationsFallback 生成达到最大迭代次数时追加到正文的收尾文案。
//
// 分两种口径：
//   - 正文为空：模型还没产出任何结论（多半一直在调工具），只能提示用户缩小范围重试；
//   - 正文非空：把已产出的内容明确标注为"部分报告"，避免用户误以为是完整结论。
func maxIterationsFallback(answer string) string {
	if strings.TrimSpace(answer) == "" {
		return maxIterationsNoticeKey + "。系统已停止继续调用工具，以避免陷入循环；" +
			"请缩小问题范围后重试，例如明确项目路径、模块、函数、接口 URL 或要执行的具体命令。"
	}
	return "\n\n" + maxIterationsNoticeKey + "，系统已停止继续调用工具。" +
		"以上内容基于当前已返回结果生成，未确认部分请缩小范围后继续追问。"
}

// handleMaxIterationsExceeded 处理"超过最大迭代次数"：把兜底文案当作正文的一部分下发。
//
// 关键点是**不返回错误**。返回错误会让 executeChat 走失败分支，用户看到红色错误气泡、
// 拿不到已经生成的内容；这里返回 nil，上层会照常发 done 事件并把本轮落库。
func (s *Service) handleMaxIterationsExceeded(
	ctx context.Context,
	event *adk.AgentEvent,
	runState *dto.ChatRunState,
	emit ChatEmit) error {
	if runState == nil {
		return nil
	}
	// 幂等：子 Agent 超限会向上冒泡，父 Agent 可能再次收到同类错误，只收尾一次。
	if strings.Contains(runState.Answer, maxIterationsNoticeKey) {
		return nil
	}
	message := maxIterationsFallback(runState.Answer)
	runState.Answer += message
	// 超限收尾产出的是"部分报告"，不是完整结论：标记降级，
	// 落库后 run.degraded 可以为前端/后续分析区分这份结果的可信范围。
	runState.Degraded = true
	// 同时写明降级原因。只置布尔位的话，"迭代耗尽"与 executeRun 里
	// "报错但留了半成品"的降级会混成同一个值，报表就无法归因到
	// 该调 max_iterations 还是该修报错。
	runState.DegradedReason = dto.DegradedReasonMaxIterations
	agentName := ""
	if event != nil {
		agentName = event.AgentName
	}
	return emitMarkDownBlock(emit, runState, consts.SseEventTypeDelta, agentName, message)
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
					// 只记录定位需要的标量字段：runState 里的 render_events 会随轮次
					// 累积，每次 EOF 都把整份 runState（以及整段 message）序列化进日志
					// 是 O(n²) 的日志放大，单轮就能到 MB 级。
					agentName, runID, sessionID, answerRunes, renderEvents := "", "", "", 0, 0
					if event != nil {
						agentName = event.AgentName
					}
					if runState != nil {
						runID = runState.RunID
						sessionID = runState.SessionID
						answerRunes = len([]rune(runState.Answer))
						renderEvents = len(runState.RenderEvents)
					}
					logger.Info("consumeMessageStream EOF agent=%s run_id=%s session_id=%s answer_runes=%d message_runes=%d render_events=%d",
						agentName, runID, sessionID, answerRunes, len([]rune(fullMsg)), renderEvents)
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
	err := s.trackUsage(ctx, runState, msg, event.AgentName)
	if err != nil {
		logger.Error("handleMessage trackUsage error", runStateBrief(runState), zap.Any("err", err))
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
	// 工具失败率：本项目的工具把错误当正常结果返回（见 looksLikeToolError 注释），
	// eino 层看不到 Go error，只能在这里按措辞判定。
	if looksLikeToolError(msg.Content) {
		runState.ToolErrors++
	}
	// 工具执行结果必须用 tool_result 事件下发：前端按事件名分发，
	// 事件名若仍是 tool_call，卡片会一直停在“调用中”，tool_result 字段也不会被渲染。
	// 该事件同时会写入 runState.RenderEvents 供历史回放，实时流与回放必须同名。
	emitEvent := dto.ChatStreamEvent{
		Type:        consts.SseEventTypeToolResult,
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
			logger.Error("handleMessage emitEvent error", zap.Any("event", event), runStateBrief(runState), zap.Error(err))
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
		logger.Error("handleToolCall error", zap.Any("event", event), runStateBrief(runState), zap.Error(err))

		return err
	}

	// 调用工具
	for _, call := range msg.ToolCalls {
		toolName := call.Function.Name
		// 工具调用次数：一次 run 里模型实际发起的工具调用总数。
		runState.ToolCalls++
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
				logger.Error("handleToolCall emitEvent error", zap.Any("event", event), runStateBrief(runState), zap.Error(err))
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
		info, ok := infoFromInterruptFunc(interruptCtx.Info)
		if !ok {
			continue
		}
		if s.approvals != nil {
			err := s.approvals.BindInterrupt(ctx, info.PendingID, info.CheckpointID, interruptCtx.ID)
			if err != nil {
				logger.Error("handleInterrupt error", zap.Any("event", event), runStateBrief(runState), zap.Any("info", info), zap.Error(err))
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
				logger.Error("handleInterruptedEvent interruptedEvent emit error", zap.Any("event", event), runStateBrief(runState), zap.Error(err))
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
				logger.Error("handleInterruptedEvent pauseEvent emit error", zap.Any("event", event), runStateBrief(runState), zap.Error(err))
				return err
			}
		}
		return nil
	}
	return nil
}
