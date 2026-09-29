package dto

type ListSession struct {
	Pager
}

type ListSessionResp struct {
	Total   int64             `json:"total"`
	List    []*SessionContext `json:"list"`
	HasMore bool              `json:"has_more"`
	Pager
}

type SessionInfo struct {
	Session *SessionContext      `json:"session"`
	List    []*ChatMessageRecord `json:"list"`
	Total   int64                `json:"total"`
	// HasMore/Pager 供前端做历史消息分页：缺少这两个字段时前端拿不到
	// has_more、page、limit，只能一直停留在第一页。
	HasMore bool `json:"has_more"`
	Pager
}

type GetSessionInfo struct {
	SessionID string `json:"session_id" form:"session_id"`
	Pager
}
