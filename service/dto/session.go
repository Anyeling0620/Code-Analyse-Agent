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
	// Total 不再统计：历史接口用 limit+1 判断 has_more，避免每页都做 COUNT(*)。
	// 保留字段只为兼容旧前端，值为 0 时通过 omitempty 省略。
	Total int64 `json:"total,omitempty"`
	// HasMore/Pager 供前端做历史消息分页：缺少这两个字段时前端拿不到
	// has_more、page、limit，只能一直停留在第一页。
	HasMore bool `json:"has_more"`
	Pager
}

type GetSessionInfo struct {
	SessionID string `json:"session_id" form:"session_id"`
	// IncludeRenderEvents 默认 false：只返回正文和 has_render_events 标记，
	// 避免把几十 MB 的调用事件随首屏一起返回。需要完整事件时显式传
	// include_render_events=1（老前端兼容路径），或按消息调用详情接口。
	IncludeRenderEvents bool `json:"include_render_events" form:"include_render_events"`
	Pager
}

// GetMessage 按单条消息读取调用事件，供前端点击“调用详情”时懒加载。
//
// 默认返回“可渲染事件”：丢掉前端回放会忽略的 reason/空 progress/空参数片段，
// 并合并连续 delta（前端本来也会把它们合并成同一个文本块）。这样最大的一条
// 39MB 消息可以降到几百 KB。需要原始事件时显式传 raw=1。
type GetMessage struct {
	SessionID string `json:"session_id" form:"session_id"`
	MessageID uint   `json:"message_id" form:"message_id" binding:"required"`
	Raw       bool   `json:"raw" form:"raw"`
}
