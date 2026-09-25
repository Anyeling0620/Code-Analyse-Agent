package conversation

import (
	"context"
	"edu.agent.code/common"
	"edu.agent.code/service/dto"
	"edu.agent.code/utils/logger"
	"github.com/jinzhu/copier"
)

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
		Total: total,
		List:  results,
		Pager: req.Pager,
	}, nil
}

func (s *Service) GetSessionInfo(ctx context.Context, userID string, req *dto.GetSessionInfo) (*dto.SessionInfo, error) {
	session, err := s.sessions.GetByID(ctx, req.SessionID)
	if err != nil {
		logger.Error("GetSessionInfo GetByID error=%v user=%s req=%+v", err, userID, req)
		return nil, err
	}
	if session.UserID != userID {
		logger.Warn("GetSessionInfo GetByID this session not own user=%s req=%+v", userID, req)
		return nil, nil
	}
	messages, total, err := s.sessions.ListMessages(ctx, userID, req.SessionID, req.Pager)
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
		Total:   total,
	}, err

}
