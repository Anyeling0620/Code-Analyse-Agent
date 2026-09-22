package model

import (
	"time"
)

// Profile 是用户画像表的 GORM 模型。
type Profile struct {
	UserID           string   `gorm:"primaryKey;size:128"`
	AuthSubject      string   `gorm:"size:128;index"`
	UserType         string   `gorm:"size:64"`
	SkillLevel       string   `gorm:"size:64"`
	GoalType         string   `gorm:"size:64"`
	PurchasedCourses []string `gorm:"serializer:json"`
	CurrentTopic     string   `gorm:"size:255"`
	CurrentStage     string   `gorm:"size:128"`
	UpdatedAt        time.Time
}

// TableName 返回用户画像表名。
func (Profile) TableName() string {
	return "profiles"
}

// Session 是会话摘要表的 GORM 模型。
type Session struct {
	SessionID          string `gorm:"primaryKey;size:128"`
	UserID             string `gorm:"size:128;index"`
	SessionOwner       string `gorm:"size:128;index"`
	Summary            string `gorm:"type:text"`
	LastUserMessage    string `gorm:"type:text"`
	LastAssistantMsg   string `gorm:"type:text"`
	CurrentProjectRoot string `gorm:"type:text"`
	CurrentProjectName string `gorm:"size:255"`
	UpdatedAt          time.Time
}

// TableName 返回会话摘要表名。
func (Session) TableName() string {
	return "sessions"
}

// // NewSession 把会话 DTO 转成 GORM 模型。
//
//	func NewSession(session *dto.SessionContext) *Session {
//		if session == nil {
//			return nil
//		}
//
//		return &Session{
//			SessionID:          session.SessionID,
//			UserID:             session.UserID,
//			SessionOwner:       session.SessionOwner,
//			Summary:            session.Summary,
//			LastUserMessage:    session.LastUserMessage,
//			LastAssistantMsg:   session.LastAssistantMsg,
//			CurrentProjectRoot: session.CurrentProjectRoot,
//			CurrentProjectName: session.CurrentProjectName,
//			UpdatedAt:          session.UpdatedAt,
//		}
//	}
//
// // ToDomain 把 GORM 模型转成会话 DTO。
//
//	func (m *Session) ToDomain() *dto.SessionContext {
//		if m == nil {
//			return nil
//		}
//
//		return &dto.SessionContext{
//			SessionID:          m.SessionID,
//			UserID:             m.UserID,
//			SessionOwner:       m.SessionOwner,
//			Summary:            m.Summary,
//			LastUserMessage:    m.LastUserMessage,
//			LastAssistantMsg:   m.LastAssistantMsg,
//			CurrentProjectRoot: m.CurrentProjectRoot,
//			CurrentProjectName: m.CurrentProjectName,
//			UpdatedAt:          m.UpdatedAt,
//		}
//	}
//
// ChatMessage 是会话消息表的 GORM 模型。
type ChatMessage struct {
	ID           uint   `gorm:"primaryKey"`
	SessionID    string `gorm:"size:128;index"`
	UserID       string `gorm:"size:128;index"`
	Role         string `gorm:"size:32;index"`
	Content      string `gorm:"type:text"`
	RenderEvents string `gorm:"type:text"`
	CreatedAt    time.Time
}

// TableName 返回会话消息表名。
func (ChatMessage) TableName() string {
	return "chat_messages"
}

//
//// NewChatMessage 把消息 DTO 转成 GORM 模型。
//func NewChatMessage(message dto.ChatMessageRecord) *ChatMessage {
//	return &ChatMessage{
//		ID:           message.ID,
//		SessionID:    message.SessionID,
//		UserID:       message.UserID,
//		Role:         message.Role,
//		Content:      message.Content,
//		RenderEvents: marshalRenderEvents(message.RenderEvents),
//		CreatedAt:    message.CreatedAt,
//	}
//}
//
//// ToDomain 把 GORM 模型转成消息 DTO。
//func (m *ChatMessage) ToDomain() dto.ChatMessageRecord {
//	if m == nil {
//		return dto.ChatMessageRecord{}
//	}
//
//	return dto.ChatMessageRecord{
//		ID:           m.ID,
//		SessionID:    m.SessionID,
//		UserID:       m.UserID,
//		Role:         m.Role,
//		Content:      m.Content,
//		RenderEvents: unmarshalRenderEvents(m.RenderEvents),
//		CreatedAt:    m.CreatedAt,
//	}
//}
//
//func marshalRenderEvents(events []dto.ChatStreamEvent) string {
//	if len(events) == 0 {
//		return ""
//	}
//	data, err := json.Marshal(events)
//	if err != nil {
//		return ""
//	}
//	return string(data)
//}
//
//func unmarshalRenderEvents(raw string) []dto.ChatStreamEvent {
//	if raw == "" {
//		return nil
//	}
//	var events []dto.ChatStreamEvent
//	if err := json.Unmarshal([]byte(raw), &events); err != nil {
//		return nil
//	}
//	return events
//}
