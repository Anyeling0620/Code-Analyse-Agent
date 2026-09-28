package model

import "time"

// SessionShare 是会话只读分享记录的 GORM 模型。
// Payload 存分享创建那一刻的会话快照（JSON），之后会话继续对话不影响已生成的分享。
type SessionShare struct {
	ID         uint   `gorm:"primaryKey"`
	ShareToken string `gorm:"size:64;uniqueIndex"`
	SessionID  string `gorm:"size:128;index"`
	UserID     string `gorm:"size:128;index"`
	Payload    string `gorm:"type:text"`
	Revoked    bool   `gorm:"index"`
	ExpiresAt  *time.Time
	CreatedAt  time.Time
}

// TableName 返回会话分享表名。
func (SessionShare) TableName() string {
	return "session_shares"
}
