package consts

const (
	// 基础任务
	ComposeMaxIterations      = 30
	ProjectQAMaxIterations    = 10
	RepoAnalysisMaxIterations = 20

	// 子任务
	RepoAnalysisSubAgentMaxIterations = 8
	DBReportMaxIterations             = 12
)

const (
	// ChatStreamEvent字段常量
	// ready/heartbeat/session/observe/progress/tool_call/tool_result/delta/interrupt/done/error
	SseEventTypeReady      = "ready"
	SseEventTypeHeartbeat  = "heartbeat"
	SseEventTypeSession    = "session"
	SseEventTypeObserve    = "observe"
	SseEventTypeProgress   = "progress"
	SseEventTypeToolCall   = "tool_call"
	SseEventTypeToolResult = "tool_result"
	SseEventTypeDelta      = "delta"
	SseEventTypeReason     = "reason"
	SseEventTypeInterrupt  = "interrupt"
	SseEventTypeDone       = "done"
	SseEventTypeError      = "error"
)

const (
	PendingStatusPending   = "pending"
	PendingStatusApproval  = "approved"
	PendingStatusRejected  = "rejected"
	PendingStatusCancelled = "executed"
	PendingStatusFailed    = "failed"
)
