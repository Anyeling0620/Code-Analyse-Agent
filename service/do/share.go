package do

import "time"

// SessionShare 是会话只读分享记录的领域对象。
// Payload 存的是分享创建那一刻的快照（JSON），之后会话继续对话不会影响它。
type SessionShare struct {
	ID         uint
	ShareToken string
	SessionID  string
	UserID     string
	Payload    string
	Revoked    bool
	ExpiresAt  *time.Time
	CreatedAt  time.Time
}
