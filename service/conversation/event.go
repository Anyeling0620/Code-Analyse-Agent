package conversation

import (
	"context"
	"edu.agent.code/service/consts"
	"edu.agent.code/service/dto"
	"edu.agent.code/utils/logger"
	"errors"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	"io"
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
			return nil
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		output := event.Output.MessageOutput
		if output.IsStreaming && output.MessageStream != nil {
			// 处理流式内容
			stream := output.MessageStream
			fullMsg := ""
			for {
				msg, err := stream.Recv()

				if errors.Is(err, io.EOF) {
					break
				}
				fullMsg = fullMsg + msg.Content

			}
			emit(dto.ChatStreamEvent{
				Type:  consts.SseEventTypeProgress,
				Delta: fullMsg,
			})

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

func (s *Service) handleMessage(
	ctx context.Context,
	msg *schema.Message,
	output *adk.TypedMessageVariant[*schema.Message],
	event *adk.AgentEvent,
	runState *dto.ChatRunState,
	emit ChatEmit,
) error {
	return nil
}
