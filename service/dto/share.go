package dto

import "time"

// CreateShareRequest 是创建只读分享的请求体。
type CreateShareRequest struct {
	SessionID string `json:"session_id" form:"session_id"`
}

// CreateShareResult 是创建只读分享的返回体。
// SharePath 是可直接拼到站点根上的相对路径（前端再补 origin）。
type CreateShareResult struct {
	ShareToken string     `json:"share_token"`
	SharePath  string     `json:"share_path"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

// GetSharedSession 是读取只读分享的查询参数。
type GetSharedSession struct {
	ShareToken string `json:"share_token" form:"share_token"`
}

// SharedSessionInfo 是只读分享页拿到的完整数据，全部来自分享时刻的快照。
type SharedSessionInfo struct {
	Session   *SessionContext      `json:"session"`
	List      []*ChatMessageRecord `json:"list"`
	Total     int64                `json:"total"`
	SharedAt  time.Time            `json:"shared_at"`
	ExpiresAt *time.Time           `json:"expires_at"`
}

// ShareSnapshot 是落库的快照结构，独立于对外响应，便于以后调整响应字段。
type ShareSnapshot struct {
	Session *SessionContext      `json:"session"`
	List    []*ChatMessageRecord `json:"list"`
	Total   int64                `json:"total"`
}
