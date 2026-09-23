package session

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/model"
	"edu.agent.code/service/do"
	"encoding/json"
	"errors"
	"github.com/gogf/gf/v2/util/gconv"
	"github.com/samber/lo"
	"gorm.io/gorm"
	"time"
)

type ISession interface {
	GetByID(ctx context.Context, sessionID string) (*do.SessionContext, error)
	ListByUser(ctx context.Context, userID string, limit int) ([]*do.SessionContext, error)
	ListMessage(ctx context.Context, userID, sessionID string, limit int) ([]do.ChatMessageRecord, error)
	AppendMessage(ctx context.Context, msg *do.ChatMessageRecord) error
	Upsert(ctx context.Context, session *do.SessionContext) error
	Delete(ctx context.Context, userID, sessionID string) error
}

type Session struct {
	db *gorm.DB
}

func NewSession(adaptor adaptor.IAdaptor) *Session {
	return &Session{
		db: adaptor.GetDB(),
	}
}

func (s *Session) GetByID(ctx context.Context, sessionID string) (*do.SessionContext, error) {
	var row model.Session
	err := s.db.WithContext(ctx).Where("session_id = ?", sessionID).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &do.SessionContext{
		SessionID:          row.SessionID,
		UserID:             row.UserID,
		SessionOwner:       row.SessionOwner,
		Summary:            row.Summary,
		LastUserMessage:    row.LastUserMessage,
		LastAssistantMsg:   row.LastAssistantMsg,
		CurrentProjectRoot: row.CurrentProjectRoot,
		CurrentProjectName: row.CurrentProjectName,
		UpdatedAt:          row.UpdatedAt,
	}, nil
}

func truncateContent(text string) string {
	r := []rune(text)
	if len(r) > 200 {
		return string(r[0:200]) + "..."
	}
	return text
}

func (s *Session) ListByUser(cx context.Context, userID string, limit int) ([]*do.SessionContext, error) {
	if limit == 0 || limit > 100 {
		limit = 50
	}
	var rows []*model.Session
	err := s.db.WithContext(cx).
		Where("user_id = ?", userID).
		Order("updated_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var results []*do.SessionContext
	lo.ForEach(rows, func(item *model.Session, index int) {
		results = append(results, &do.SessionContext{
			SessionID:          item.SessionID,
			UserID:             item.UserID,
			SessionOwner:       item.SessionOwner,
			Summary:            truncateContent(item.Summary),
			LastUserMessage:    truncateContent(item.LastUserMessage),
			LastAssistantMsg:   truncateContent(item.LastAssistantMsg),
			CurrentProjectRoot: item.CurrentProjectRoot,
			CurrentProjectName: item.CurrentProjectName,
			UpdatedAt:          item.UpdatedAt,
		})
	})
	return results, err
}

func (s *Session) ListMessage(ctx context.Context, userID, sessionID string, limit int) ([]do.ChatMessageRecord, error) {
	if limit == 0 || limit > 100 {
		limit = 50
	}

	var rows []*model.ChatMessage
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND session_id = ?", userID, sessionID).
		Order("created_at DESC").
		Limit(limit).
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

	var results []do.ChatMessageRecord
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

	return results, err
}

func (s *Session) AppendMessage(ctx context.Context, msg *do.ChatMessageRecord) error {
	row := model.ChatMessage{
		SessionID:    msg.SessionID,
		UserID:       msg.UserID,
		Role:         msg.Role,
		Content:      msg.Content,
		RenderEvents: gconv.String(msg.RenderEvents),
		CreatedAt:    msg.CreatedAt,
	}
	return s.db.WithContext(ctx).Create(row).Error
}

func (s *Session) Upsert(ctx context.Context, session *do.SessionContext) error {
	row := model.Session{
		SessionID:          session.SessionID,
		UserID:             session.UserID,
		SessionOwner:       session.SessionOwner,
		Summary:            session.Summary,
		LastUserMessage:    session.LastUserMessage,
		LastAssistantMsg:   session.LastAssistantMsg,
		CurrentProjectRoot: session.CurrentProjectRoot,
		CurrentProjectName: session.CurrentProjectName,
		UpdatedAt:          time.Now(),
	}
	return s.db.WithContext(ctx).Save(row).Error
}

// Delete 删除Session再删除ChatMessage有异议， 可能顺序反了
func (s *Session) Delete(ctx context.Context, userID, sessionID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("user_id = ? AND session_id = ?", userID, sessionID).Delete(&model.Session{}).Error
		if err != nil {
			return err
		}
		return tx.Where("user_id = ? AND session_id = ?", userID, sessionID).Delete(&model.ChatMessage{}).Error
	})
}
