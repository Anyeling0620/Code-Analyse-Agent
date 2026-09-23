package do

import "time"

type SessionContext struct {
	SessionID          string    `json:"session_id"`
	UserID             string    `json:"user_id"`
	SessionOwner       string    `json:"session_owner"` // TODO 用于会话分享
	Summary            string    `json:"summary"`
	LastUserMessage    string    `json:"last_user_message"`
	LastAssistantMsg   string    `json:"last_assistant_msg"`
	CurrentProjectRoot string    `json:"current_project_root"`
	CurrentProjectName string    `json:"current_project_name"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type ChatResult struct {
	Answer    string          `json:"answer"`
	SessionID string          `json:"session_id"`
	UsedTools []string        `json:"used_tools"`
	Profile   *Profile        `json:"profile"`
	Session   *SessionContext `json:"session"`
}

type ChatStreamEvent struct {
	Type          string      `json:"type"` // ready / session /observe/ progress / tool_call /tool_result /delta / interrupt / done / error
	TraceID       string      `json:"trace_id"`
	SessionID     string      `json:"session_id"`
	ToolName      string      `json:"tool_name"`
	ToolCallID    string      `json:"tool_call_id"`
	ToolArguments string      `json:"tool_arguments"`
	ToolResult    string      `json:"tool_result"`
	Delta         string      `json:"delta"`
	Message       string      `json:"message"`
	Stage         string      `json:"stage"`
	Detail        string      `json:"detail"`
	ElapseMS      int64       `json:"elapse_ms"`
	Timestamp     int64       `json:"timestamp"`
	ContentKind   string      `json:"content_kind"`
	RenderMode    string      `json:"render_mode"`
	Visibility    string      `json:"visibility"`
	Result        *ChatResult `json:"result"`

	//  中断事件
	PendingApprovalID string
	PendingCommand    string
	PendingRiskReason string
	PendingRiskLevel  string
	PendingWorkDir    string
	PendingTimeoutSec int
}

type ChatMessageRecord struct {
	ID           uint
	SessionID    string
	UserID       string
	Role         string
	Content      string
	RenderEvents []ChatStreamEvent
	CreatedAt    time.Time
}
