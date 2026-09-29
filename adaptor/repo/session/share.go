package session

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/model"
	"edu.agent.code/service/do"
	"encoding/json"
	"errors"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

// IShare 是会话只读分享的仓储。
type IShare interface {
	Create(ctx context.Context, item *do.SessionShare) error
	GetByToken(ctx context.Context, token string) (*do.SessionShare, error)
	RevokeBySession(ctx context.Context, userID, sessionID string) error
	ListAllMessages(ctx context.Context, sessionID string) ([]do.ChatMessageRecord, error)
}

type Share struct {
	db *gorm.DB
}

func NewShare(adaptor adaptor.IAdaptor) *Share {
	return &Share{db: adaptor.GetDB()}
}

// NewShareWithDB 用现成的 *gorm.DB 构造仓储，供单测复用。
func NewShareWithDB(db *gorm.DB) *Share {
	return &Share{db: db}
}

func (s *Share) Create(ctx context.Context, item *do.SessionShare) error {
	row := model.SessionShare{
		ShareToken: item.ShareToken,
		SessionID:  item.SessionID,
		UserID:     item.UserID,
		Payload:    item.Payload,
		Revoked:    item.Revoked,
		ExpiresAt:  item.ExpiresAt,
		CreatedAt:  item.CreatedAt,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return err
	}
	item.ID = row.ID
	return nil
}

func (s *Share) GetByToken(ctx context.Context, token string) (*do.SessionShare, error) {
	var row model.SessionShare
	err := s.db.WithContext(ctx).Where("share_token = ?", token).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &do.SessionShare{
		ID:         row.ID,
		ShareToken: row.ShareToken,
		SessionID:  row.SessionID,
		UserID:     row.UserID,
		Payload:    row.Payload,
		Revoked:    row.Revoked,
		ExpiresAt:  row.ExpiresAt,
		CreatedAt:  row.CreatedAt,
	}, nil
}

// RevokeBySession 把某个用户某个会话下所有未失效的分享标记为已撤销。
// 条件里带 user_id，保证只能撤销自己的分享。
func (s *Share) RevokeBySession(ctx context.Context, userID, sessionID string) error {
	return s.db.WithContext(ctx).Model(&model.SessionShare{}).
		Where("user_id = ? AND session_id = ? AND revoked = ?", userID, sessionID, false).
		Update("revoked", true).Error
}

// ListAllMessages 取会话全部消息，不带 user 过滤也不分页，供分享快照使用。
// 调用方必须已经确认过会话归属。
func (s *Share) ListAllMessages(ctx context.Context, sessionID string) ([]do.ChatMessageRecord, error) {
	var rows []*model.ChatMessage
	err := s.db.WithContext(ctx).Model(&model.ChatMessage{}).
		Where("session_id = ?", sessionID).
		Order("created_at ASC, id ASC").
		Find(&rows).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}

	unmarshalRenderEventFun := func(row string) []do.ChatStreamEvent {
		if row == "" {
			return nil
		}
		var events []do.ChatStreamEvent
		_ = json.Unmarshal([]byte(row), &events)
		return events
	}

	results := make([]do.ChatMessageRecord, 0, len(rows))
	lo.ForEach(rows, func(item *model.ChatMessage, index int) {
		results = append(results, do.ChatMessageRecord{
			ID:           item.ID,
			SessionID:    item.SessionID,
			UserID:       item.UserID,
			Role:         item.Role,
			Content:      item.Content,
			RenderEvents: unmarshalRenderEventFun(item.RenderEvents),
			CreatedAt:    item.CreatedAt,
		})
	})
	return results, nil
}
