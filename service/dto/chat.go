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
	UserID       string    `json:"user_id"`
	AuthSubject  string    `json:"auth_subject"`
	UserType     string    `json:"user_type"`
	SkillLevel   string    `json:"skill_level"`
	GoalType     string    `json:"goal_type"`
	Description  string    `json:"description"`
	CurrentTopic string    `json:"current_topic"`
	CurrentStage string    `json:"current_stage"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type ProfileUpdateReq struct {
	UserType     string `json:"user_type"`
	SkillLevel   string `json:"skill_level"`
	GoalType     string `json:"goal_type"`
	Description  string `json:"description"`
	CurrentTopic string `json:"current_topic"`
	CurrentStage string `json:"current_stage"`
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
	Type string `json:"type"` // ready / session /observe/ progress / tool_call /tool_result /delta / interrupt / done / error
	// RunID / Seq 是断连续传的锚点：Seq 对应 chat_run_events.id，
	// 客户端按 Last-Event-ID 重连时只补 seq 之后的事件。
	RunID         string      `json:"run_id"`
	Seq           int64       `json:"seq"`
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
	ElapseMS      int64       `json:"elapsed_ms"`
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

// ChatRunState.DegradedReason 的取值。这两个值会落到 agent_telemetry.run_metrics，
// 改动等于改报表口径，必须同步更新那边的注释。
const (
	// DegradedReasonMaxIterations 表示命中迭代上限后强制收尾。
	DegradedReasonMaxIterations = "max_iterations"
	// DegradedReasonErrorPartial 表示执行报错但保留了已产出的半成品。
	DegradedReasonErrorPartial = "error_partial"
)

type ChatRunState struct {
	UserID            string                   `json:"user_id"`
	RunID             string                   `json:"run_id"`
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
	// Degraded 标记本轮是"降级完成"：例如达到最大工具轮次后只输出部分报告，
	// 或出错但仍保留了半成品内容。落库后可直接用来区分完整结论与部分结论。
	Degraded bool `json:"degraded"`
	// DegradedReason 区分 Degraded 的原因，避免"迭代耗尽"和"报错但有半成品"
	// 共用一个布尔位而无法归因：
	//   DegradedReasonMaxIterations —— 命中迭代上限后强制收尾；
	//   DegradedReasonErrorPartial  —— 执行报错但保留了已产出的半成品。
	DegradedReason string `json:"degraded_reason"`
	// LastSeq 是本轮已落库的最后一个事件 seq，审批恢复时用它作为续传起点。
	LastSeq int64 `json:"last_seq"`

	// 以下字段是埋点统计，只累计数字，不承载对话原文。
	// StartedAt 取 run 创建时间；被审批中断后 resume 的 run 仍是原始创建时间，
	// 所以 Duration 里包含审批等待时间。
	StartedAt time.Time `json:"started_at"`
	// LLMCalls 是模型调用次数，等价于迭代轮次（流式只有末帧带 Usage）。
	LLMCalls         int   `json:"llm_calls"`
	ToolCalls        int   `json:"tool_calls"`
	ToolErrors       int   `json:"tool_errors"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	// CostCNY 是本轮累计的人民币成本（由 cost.Track 逐次累加）。
	CostCNY float64 `json:"cost_cny"`
}

// ChatRunInfo 是对外的 run 摘要（不含 question/answer 大字段以外的内部数据）。
type ChatRunInfo struct {
	RunID             string `json:"run_id"`
	SessionID         string `json:"session_id"`
	Status            string `json:"status"`
	Degraded          bool   `json:"degraded"`
	Question          string `json:"question"`
	PendingApprovalID string `json:"pending_approval_id"`
	ProjectRoot       string `json:"project_root"`
	ProjectName       string `json:"project_name"`
	LastSeq           int64  `json:"last_seq"`
	StartedAt         string `json:"started_at"`
	EndedAt           string `json:"ended_at"`
}

type ChatRunActiveReq struct {
	SessionID string `form:"session_id" json:"session_id"`
}

type ChatRunActiveResp struct {
	Run *ChatRunInfo `json:"run"`
}

type ChatResumeRequest struct {
	UserID    string `json:"user_id"`
	TraceID   string `json:"trace_id"`
	SessionID string `json:"session_id"`
	PendingID string `json:"pending_id"`
	Approved  bool   `json:"approved"`
}
