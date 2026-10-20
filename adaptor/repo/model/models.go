package model

import (
	"time"
)

// Profile 是用户画像表的 GORM 模型。
// Description 是自由文本的自我介绍（原先的 purchased_courses 数组列保留在库里，
// 仅作历史数据回退用，不再读写）。
type Profile struct {
	UserID       string `gorm:"primaryKey;size:128"`
	AuthSubject  string `gorm:"size:128;index"`
	UserType     string `gorm:"size:64"`
	SkillLevel   string `gorm:"size:64"`
	GoalType     string `gorm:"size:64"`
	Description  string `gorm:"type:text"`
	CurrentTopic string `gorm:"size:255"`
	CurrentStage string `gorm:"size:128"`
	UpdatedAt    time.Time
}

// TableName 返回用户画像表名。
func (Profile) TableName() string {
	return "profiles"
}

// ProfileRevision 是用户画像的历史快照表。
// 每次 profiles 实际变更时，在同一事务里追加一行；profiles 仍只保存当前值。
type ProfileRevision struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement"`
	UserID       string    `gorm:"size:128;index:idx_profile_revisions_user_time,priority:1"`
	UserType     string    `gorm:"size:64"`
	SkillLevel   string    `gorm:"size:64"`
	GoalType     string    `gorm:"size:64"`
	Description  string    `gorm:"type:text"`
	CurrentTopic string    `gorm:"size:255"`
	CurrentStage string    `gorm:"size:128"`
	Source       string    `gorm:"size:32"`
	UpdatedAt    time.Time `gorm:"index:idx_profile_revisions_user_time,priority:2"`
}

// TableName 返回画像快照表名。
func (ProfileRevision) TableName() string {
	return "profile_revisions"
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
//
// 复合索引 idx_chat_messages_user_session_created 精确匹配历史接口的
// "按用户+会话过滤、按时间倒序分页"查询；只有单列索引时 SQLite 需要
// 先取候选行再排序，深页会随数据量变慢。AutoMigrate 会幂等创建该索引。
type ChatMessage struct {
	ID        uint      `gorm:"primaryKey;index:idx_chat_messages_user_session_created,priority:4"`
	SessionID string    `gorm:"size:128;index;index:idx_chat_messages_user_session_created,priority:2"`
	UserID    string    `gorm:"size:128;index;index:idx_chat_messages_user_session_created,priority:1"`
	Role      string    `gorm:"size:32;index"`
	Content   string    `gorm:"type:text"`
	CreatedAt time.Time `gorm:"index:idx_chat_messages_user_session_created,priority:3"`

	RenderEvents string `gorm:"type:text"`
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
