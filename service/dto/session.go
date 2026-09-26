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
}

type GetSessionInfo struct {
	SessionID string `json:"session_id" form:"session_id"`
	Pager
}
