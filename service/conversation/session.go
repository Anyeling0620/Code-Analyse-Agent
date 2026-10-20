package conversation

import (
	"context"
	"edu.agent.code/common"
	"edu.agent.code/service/agent/compress"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"
	"edu.agent.code/utils/logger"
	"errors"
	"github.com/cloudwego/eino/schema"
	"github.com/jinzhu/copier"
	"go.uber.org/zap"
	"time"
)

// ErrMessageNotFound 表示消息不存在，或不属于当前用户/会话。
// 两种情况统一成一个错误，避免通过错误信息探测他人消息是否存在。
var ErrMessageNotFound = errors.New("message not found")

func (s *Service) DeleteSession(ctx context.Context, userID, sessionID string) error {
	err := s.sessions.Delete(ctx, userID, sessionID)
	if err != nil {
		logger.Error("DeleteSession Delete error=%v user=%s session=%s", err, userID, sessionID)
		return err
	}
	return nil
}

func (s *Service) ListSession(ctx context.Context,
	user *common.UserInfo, req *dto.ListSession) (*dto.ListSessionResp, error) {
	items, total, err := s.sessions.ListByUser(ctx, user.UserID, req.Pager)
	if err != nil {
		logger.Error("ListSession ListByUser error=%v user=%s req=%+v", err, user, req)
		return nil, err
	}
	results := make([]*dto.SessionContext, 0, len(items))
	_ = copier.Copy(&results, &items)
	return &dto.ListSessionResp{
		Total:   total,
		List:    results,
		HasMore: int64(req.GetOffset()+len(items)) < total,
		Pager:   req.Pager,
	}, nil
}

func (s *Service) GetSessionInfo(ctx context.Context, userID string, req *dto.GetSessionInfo) (*dto.SessionInfo, error) {
	session, err := s.sessions.GetByID(ctx, req.SessionID)
	if err != nil {
		logger.Error("GetSessionInfo GetByID error=%v user=%s req=%+v", err, userID, req)
		return nil, err
	}
	if session == nil || session.UserID != userID {
		logger.Warn("GetSessionInfo GetByID this session not own user=%s req=%+v", userID, req)
		return nil, nil
	}
	// 先归一化 page/limit：ListMessages 收到的是 Pager 的值拷贝，
	// 它内部的 GetOffset 不会回写到 req.Pager，响应里的分页参数需要在这里生效。
	_ = req.GetOffset()
	// 默认不读 render_events：单条消息的事件最多接近 40MB，首屏只需要正文和
	// has_render_events 标记。旧调用方可以显式传 include_render_events=1 取回完整数据。
	messages, hasMore, err := s.sessions.ListMessages(ctx, userID, req.SessionID, req.Pager, req.IncludeRenderEvents)
	if err != nil {
		logger.Error("GetSessionInfo ListMessages error=%v user=%s req=%+v", err, userID, req)
		return nil, err
	}
	resultSession := &dto.SessionContext{}
	copier.Copy(&resultSession, &session)
	resultMessages := make([]*dto.ChatMessageRecord, 0, len(messages))
	copier.Copy(&resultMessages, &messages)
	return &dto.SessionInfo{
		Session: resultSession,
		List:    resultMessages,
		HasMore: hasMore,
		Pager:   req.Pager,
	}, nil

}

// GetMessage 返回单条消息的完整调用事件。仓储层在 WHERE 里同时限定
// user_id 与 session_id，消息不存在和越权访问返回同一个错误。
func (s *Service) GetMessage(ctx context.Context, userID string, req *dto.GetMessage) (*dto.ChatMessageRecord, error) {
	message, err := s.sessions.GetMessage(ctx, userID, req.SessionID, req.MessageID)
	if err != nil {
		logger.Error("GetMessage repo error=%v user=%s session=%s message=%d", err, userID, req.SessionID, req.MessageID)
		return nil, err
	}
	if message == nil {
		return nil, ErrMessageNotFound
	}
	if !req.Raw {
		compacted := *message
		compacted.RenderEvents = compactRenderEvents(message.RenderEvents)
		// 压缩后可能一条可渲染事件都不剩（例如整轮只有 reasoning），
		// 此时告诉前端“没有可加载的详情”，避免按钮点了没反应。
		compacted.HasRenderEvents = len(compacted.RenderEvents) > 0
		message = &compacted
	}
	result := &dto.ChatMessageRecord{}
	_ = copier.Copy(result, message)
	return result, nil
}

func (s *Service) persistSession(ctx context.Context, session *dto.SessionContext, runState *dto.ChatRunState) error {
	if runState == nil {
		return nil
	}
	if runState.Question != "" {
		err := s.sessions.AppendMessage(ctx, &do.ChatMessageRecord{
			SessionID: session.SessionID,
			UserID:    session.UserID,
			Role:      string(schema.User),
			Content:   runState.Question,
			CreatedAt: time.Now(),
		})
		if err != nil {
			logger.Error("persistSession AppendMessage error", zap.Any("session", session), runStateBrief(runState))
			return err
		}
		if runState.Answer != "" {
			renderEvents := make([]do.ChatStreamEvent, 0, len(runState.RenderEvents))
			_ = copier.Copy(&renderEvents, &runState.RenderEvents)
			err = s.sessions.AppendMessage(ctx, &do.ChatMessageRecord{
				SessionID:    session.SessionID,
				UserID:       session.UserID,
				Role:         string(schema.Assistant),
				Content:      runState.Answer,
				RenderEvents: renderEvents,
				CreatedAt:    time.Now(),
			})
			if err != nil {
				logger.Error("persistSession AppendMessage error", zap.Any("session", session), runStateBrief(runState), zap.Any("renderEvents", renderEvents))
				return err
			}
		}
	}
	session.SessionID = runState.SessionID
	session.LastUserMessage = runState.Question
	session.LastAssistantMsg = runState.Answer
	// 摘要的写入方式跟随压缩开关：
	//   - 开启压缩：滚动累积而不是每轮覆盖——覆盖式摘要会把更早轮次的目标与结论丢掉，
	//     而跨轮压缩记忆恰恰依赖这些信息（关闭时摘要根本不会注入，见 resolveSessionSummaryMessage）；
	//   - 关闭压缩：保持改造前的覆盖式写法，确保"关掉开关 = 行为完全不变"。
	if compress.Enabled(s.compactConf()) {
		session.Summary = mergeSessionSummary(session.Summary, runState.Question, runState.Answer)
	} else {
		session.Summary = summarySession(runState.Question, runState.Answer)
	}

	doSession := do.SessionContext{}
	_ = copier.Copy(&doSession, session)

	if err := s.sessions.Upsert(ctx, &doSession); err != nil {
		logger.Error("persistSession Upsert error", zap.Any("session", session), runStateBrief(runState), zap.Any("doSession", doSession))
		return err
	}
	return nil
}
