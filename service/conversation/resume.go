package conversation

import (
	"context"
	"edu.agent.code/common"
	"edu.agent.code/service/consts"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"
	"edu.agent.code/service/tool/terminal"
	"edu.agent.code/utils/logger"
	"fmt"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/jinzhu/copier"
	"go.uber.org/zap"
	"time"
)

func (s *Service) ResumeChat(ctx context.Context, req *dto.ChatResumeRequest, emit ChatEmit) error {
	if emit == nil {
		return fmt.Errorf("Emit cannot be nil ")
	}
	if req.PendingID == "" {
		logger.Error("ResumeChat: Pending ID is empty", zap.Any("req", req))
		return fmt.Errorf("PendingID is empty")
	}
	pending, err := s.approvals.GetByID(ctx, req.PendingID)
	if err != nil {
		logger.Error("ResumeChat: GetByID", zap.Any("req", req), zap.Error(err))
		return err
	}
	if req.SessionID != pending.SessionID {
		logger.Error("ResumeChat: SessionID mismatch", zap.Any("req", req), zap.Any("pending", pending))
		return fmt.Errorf("SessionID mismatch")
	}
	// 归属校验：pending_id 由客户端携带，凭它就能代为批准会让账号之间互相执行命令。
	if pending.UserID != req.UserID {
		logger.Error("ResumeChat: pending approval not owned by user", zap.Any("req", req), zap.Any("pending", pending))
		return fmt.Errorf("pending approval not owned by user")
	}
	if pending.CheckPointID == "" || pending.InterruptID == "" {
		logger.Error("ResumeChat: pending ID is empty", zap.Any("req", req))
		return fmt.Errorf("pending checkpointID mismatcha or interrput id mismatch")
	}

	_ = emit(dto.ChatStreamEvent{
		Type:      consts.SseEventTypeSession,
		TraceID:   req.TraceID,
		SessionID: pending.SessionID,
		Stage:     "resume_chat_started",
	})

	ctx = common.WithUserAndSession(ctx, pending.UserID, pending.SessionID)
	ctx = common.WithCheckPointID(ctx, pending.CheckPointID)
	iter, err := s.composeRunner.ResumeWithParams(ctx, pending.CheckPointID, &adk.ResumeParams{
		Targets: map[string]any{
			pending.InterruptID: terminal.ApprovalDecision{
				PendingID: pending.ID,
				Approved:  req.Approved,
			},
		},
	})
	if err != nil {
		logger.Error("ResumeChat: composeRunner failed", zap.Any("req", req), zap.Error(err))
		return err
	}
	runState := dto.ChatRunState{
		UserID:      req.UserID,
		TraceID:     req.TraceID,
		SessionID:   req.SessionID,
		UsedTools:   []string{pending.Tool},
		ToolCallMap: make(map[string]dto.ToolCallState),
	}
	err = s.consumeAgentEvents(ctx, iter, &runState, emit)
	if err != nil {
		logger.Error("ResumeChat: consumeAgentEvents failed", zap.Any("req", req), zap.Error(err))
		return err
	}
	answer := runState.Answer
	if answer == "" {
		if req.Approved {
			answer = "命令已按用户批注执行完毕"
		} else {
			answer = "用户拒绝执行"
		}

		_ = emit(dto.ChatStreamEvent{
			Type:        consts.SseEventTypeDelta,
			TraceID:     req.TraceID,
			SessionID:   req.SessionID,
			Delta:       answer,
			ContentKind: "markdown",
			RenderMode:  "append_block",
			Visibility:  "user",
			Timestamp:   time.Now().Format(time.DateTime),
		})

	}
	_ = emit(dto.ChatStreamEvent{
		Type:      consts.SseEventTypeDone,
		TraceID:   req.TraceID,
		SessionID: req.SessionID,
		Result: &dto.ChatResult{
			Answer:    answer,
			SessionID: req.SessionID,
			UsedTools: runState.UsedTools,
		},
	})
	renderEvent := make([]do.ChatStreamEvent, 0, len(runState.RenderEvents))
	_ = copier.Copy(&renderEvent, &runState.RenderEvents)
	return s.sessions.AppendMessage(ctx, &do.ChatMessageRecord{
		SessionID:    req.SessionID,
		UserID:       req.UserID,
		Role:         string(schema.Assistant),
		Content:      answer,
		RenderEvents: renderEvent,
		CreatedAt:    time.Time{},
	})
}
