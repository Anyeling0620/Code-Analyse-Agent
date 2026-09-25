package dto

import (
	"time"
)

type ChatRequest struct {
	TraceID   string   `json:"trace_id"`
	UserID    string   `json:"user_id"`
	SessionID string   `json:"session_id"`
	Message   string   `json:"message"`
	Profile   *Profile `json:"profile"`
}

type Profile struct {
	UserID           string    `json:"user_id"`
	AuthSubject      string    `json:"auth_subject"`
	UserType         string    `json:"user_type"`
	SkillLevel       string    `json:"skill_level"`
	GoalType         string    `json:"goal_type"`
	PurchasedCourses []string  `json:"purchased_courses"`
	CurrentTopic     string    `json:"current_topic"`
	CurrentStage     string    `json:"current_stage"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type SessionContext struct {
	SessionID          string    `json:"session_id"`
	UserID             string    `json:"user_id"`
	SessionOwner       string    `json:"session_owner"`
	Summary            string    `json:"summary"`
	LastUserMessage    string    `json:"last_user_message"`
	LastAssistantMsg   string    `json:"last_assistant_msg"`
	CurrentProjectRoot string    `json:"current_project_root"`
	CurrentProjectName string    `json:"current_project_name"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type ChatResult struct {
	Answer           string          `json:"answer"`
	ReasoningContent string          `json:"reasoning_content"`
	SessionID        string          `json:"session_id"`
	UsedTools        []string        `json:"used_tools"`
	Profile          *Profile        `json:"profile"`
	Session          *SessionContext `json:"session"`
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
	Timestamp     string      `json:"timestamp"`
	ContentKind   string      `json:"content_kind"`
	RenderMode    string      `json:"render_mode"`
	Visibility    string      `json:"visibility"`
	Result        *ChatResult `json:"result"`

	//  中断事件
	PendingApprovalID string `json:"pending_approval_id"`
	PendingCommand    string `json:"pending_command"`
	PendingRiskReason string `json:"pending_risk_reason"`
	PendingRiskLevel  string `json:"pending_risk_level"`
	PendingWorkDir    string `json:"pending_work_dir"`
	PendingTimeoutSec int    `json:"pending_timeout_sec"`
}

type ChatMessageRecord struct {
	ID           uint              `json:"id"`
	SessionID    string            `json:"session_id"`
	UserID       string            `json:"user_id"`
	Role         string            `json:"role"`
	Content      string            `json:"content"`
	RenderEvents []ChatStreamEvent `json:"render_events"`
	CreatedAt    time.Time         `json:"created_at"`
}

type ToolCallState struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatRunState struct {
	UserID            string                   `json:"user_id"`
	TraceID           string                   `json:"trace_id"`
	SessionID         string                   `json:"session_id"`
	Question          string                   `json:"question"`
	Answer            string                   `json:"answer"`
	ReasoningContent  string                   `json:"reasoning_content"`
	UsedTools         []string                 `json:"used_tools"`
	ToolCallMap       map[string]ToolCallState `json:"tool_call_map"`
	RenderEvents      []ChatStreamEvent        `json:"render_events"`
	PendingApprovalID string                   `json:"pending_approval_id"`
	Interrupted       bool                     `json:"interrupted"`
}
