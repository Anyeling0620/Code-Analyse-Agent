package conversation

import (
	"context"
	"edu.agent.code/common"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"
	"edu.agent.code/utils/logger"
	"encoding/json"
	"errors"
	"github.com/jinzhu/copier"
	"strings"
	"time"
)

// ErrShareSessionNotFound 表示会话不存在或不属于当前用户。
var ErrShareSessionNotFound = errors.New("share session not found")

// ErrShareLinkInvalid 表示分享令牌不存在、已撤销或已过期。
var ErrShareLinkInvalid = errors.New("share link invalid")

// shareTokenRetry 是分享令牌撞号后的重试次数。
const shareTokenRetry = 5

// CreateShare 为当前用户自己的会话创建一份只读分享。
// 快照在创建时固定：之后会话继续对话，已有分享链接看到的内容不变。
func (s *Service) CreateShare(ctx context.Context, userID, sessionID string) (*dto.CreateShareResult, error) {
	if sessionID == "" {
		return nil, ErrShareSessionNotFound
	}
	session, err := s.sessions.GetByID(ctx, sessionID)
	if err != nil {
		logger.Error("CreateShare GetByID error=%v user=%s session=%s", err, userID, sessionID)
		return nil, err
	}
	if session == nil || session.UserID != userID {
		logger.Warn("CreateShare not owned user=%s session=%s", userID, sessionID)
		return nil, ErrShareSessionNotFound
	}

	messages, err := s.shares.ListAllMessages(ctx, sessionID)
	if err != nil {
		logger.Error("CreateShare ListAllMessages error=%v user=%s session=%s", err, userID, sessionID)
		return nil, err
	}

	// do -> dto：落库 JSON 里时间戳按 dto 的形态存，读回来才不会因为类型不一致丢字段。
	snapshotSession := &dto.SessionContext{}
	_ = copier.Copy(snapshotSession, &session)
	snapshotMessages := make([]*dto.ChatMessageRecord, 0, len(messages))
	_ = copier.Copy(&snapshotMessages, &messages)
	payload, err := json.Marshal(dto.ShareSnapshot{
		Session: snapshotSession,
		List:    snapshotMessages,
		Total:   int64(len(messages)),
	})
	if err != nil {
		logger.Error("CreateShare marshal snapshot error=%v user=%s session=%s", err, userID, sessionID)
		return nil, err
	}

	for i := 0; i < shareTokenRetry; i++ {
		token := common.GetUUIDHex()
		record := &do.SessionShare{
			ShareToken: token,
			SessionID:  sessionID,
			UserID:     userID,
			Payload:    string(payload),
			CreatedAt:  time.Now(),
		}
		err = s.shares.Create(ctx, record)
		if err == nil {
			return &dto.CreateShareResult{
				ShareToken: token,
				SharePath:  "/?share=" + token,
				CreatedAt:  record.CreatedAt,
				ExpiresAt:  record.ExpiresAt,
			}, nil
		}
		if !isDuplicateKeyError(err) {
			logger.Error("CreateShare create error=%v user=%s session=%s", err, userID, sessionID)
			return nil, err
		}
	}
	return nil, errors.New("create share token failed: token conflict")
}

// GetSharedSession 按令牌返回只读分享快照。调用方不需要登录。
func (s *Service) GetSharedSession(ctx context.Context, token string) (*dto.SharedSessionInfo, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrShareLinkInvalid
	}
	record, err := s.shares.GetByToken(ctx, token)
	if err != nil {
		logger.Error("GetSharedSession GetByToken error=%v token=%s", err, token)
		return nil, err
	}
	if record == nil || record.Revoked {
		return nil, ErrShareLinkInvalid
	}
	if record.ExpiresAt != nil && record.ExpiresAt.Before(time.Now()) {
		return nil, ErrShareLinkInvalid
	}

	var snapshot dto.ShareSnapshot
	if err := json.Unmarshal([]byte(record.Payload), &snapshot); err != nil {
		logger.Error("GetSharedSession unmarshal snapshot error=%v token=%s", err, token)
		return nil, err
	}
	if snapshot.List == nil {
		snapshot.List = make([]*dto.ChatMessageRecord, 0)
	}
	return &dto.SharedSessionInfo{
		Session:   snapshot.Session,
		List:      snapshot.List,
		Total:     snapshot.Total,
		SharedAt:  record.CreatedAt,
		ExpiresAt: record.ExpiresAt,
	}, nil
}

// RevokeShare 撤销当前用户某个会话下所有未失效的分享。
// 会话不属于自己时同样按成功处理：幂等，且不泄露他人会话是否存在。
func (s *Service) RevokeShare(ctx context.Context, userID, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	if err := s.shares.RevokeBySession(ctx, userID, sessionID); err != nil {
		logger.Error("RevokeShare error=%v user=%s session=%s", err, userID, sessionID)
		return err
	}
	return nil
}

// isDuplicateKeyError 判断是否唯一索引冲突。工程里没开 GORM 的 TranslateError，
// 各驱动报错文案不同，这里按关键字兜底。
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique") || strings.Contains(text, "duplicate")
}
