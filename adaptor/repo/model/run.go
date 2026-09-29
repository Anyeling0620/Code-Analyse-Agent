package model

import "time"

// ChatRun 对应 chat_runs 表：一次对话执行的状态机记录。
type ChatRun struct {
	RunID             string `gorm:"primaryKey;size:64"`
	TraceID           string `gorm:"size:64;index"`
	SessionID         string `gorm:"size:128;index"`
	UserID            string `gorm:"size:128;index"`
	Question          string `gorm:"type:text"`
	Answer            string `gorm:"type:text"`
	Status            string `gorm:"size:32;index"`
	Degraded          bool
	ErrorMsg          string `gorm:"type:text"`
	CheckpointID      string `gorm:"size:256;index"`
	PendingApprovalID string `gorm:"size:64;index"`
	ProjectRoot       string `gorm:"type:text"`
	ProjectName       string `gorm:"size:255"`
	LastSeq           int64
	StartedAt         time.Time
	UpdatedAt         time.Time
	EndedAt           *time.Time
}

// TableName 返回 run 状态表名。
func (ChatRun) TableName() string {
	return "chat_runs"
}

// ChatRunEvent 对应 chat_run_events 表：id 即 SSE 事件 seq，回放按 run_id 过滤。
type ChatRunEvent struct {
	ID        int64  `gorm:"primaryKey;autoIncrement"`
	RunID     string `gorm:"size:64;index"`
	SessionID string `gorm:"size:128;index"`
	UserID    string `gorm:"size:128;index"`
	Type      string `gorm:"size:64"`
	Payload   string `gorm:"type:text"`
	CreatedAt time.Time
}

// TableName 返回 run 事件表名。
func (ChatRunEvent) TableName() string {
	return "chat_run_events"
}
