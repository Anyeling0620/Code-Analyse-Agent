package do

import "time"

// run 状态机取值。
//
// 终态 = done / failed；running 与 interrupted 视为"活跃"（可续跑）。
const (
	RunStatusRunning     = "running"
	RunStatusInterrupted = "interrupted"
	RunStatusDone        = "done"
	RunStatusFailed      = "failed"
)

// IsTerminalRunStatus 判断 run 是否已进入终态。
func IsTerminalRunStatus(status string) bool {
	switch status {
	case RunStatusDone, RunStatusFailed:
		return true
	default:
		return false
	}
}

// ChatRun 是一次对话执行的落库状态机记录。
//
// 它把 run 的生命周期从 HTTP 请求上摘下来：请求断开只影响推送，
// run 自己跑到终态并把结果落库。
type ChatRun struct {
	RunID             string
	TraceID           string
	SessionID         string
	UserID            string
	Question          string
	Answer            string
	Status            string
	Degraded          bool
	ErrorMsg          string
	CheckpointID      string
	PendingApprovalID string
	ProjectRoot       string
	ProjectName       string
	LastSeq           int64
	StartedAt         time.Time
	UpdatedAt         time.Time
	EndedAt           *time.Time
}

// ChatRunEvent 是 run 的一条可回放事件。
//
// Seq 即 SSE 事件游标（对应 chat_run_events.id，全局自增），
// Payload 是 dto.ChatStreamEvent 的 JSON 原文；回放时由上层回填 Seq。
type ChatRunEvent struct {
	Seq       int64
	RunID     string
	SessionID string
	UserID    string
	Type      string
	Payload   []byte
	CreatedAt time.Time
}
