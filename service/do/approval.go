package do

import "time"

type PendingApproval struct {
	ID           string
	UserID       string
	SessionID    string
	Tool         string // 哪个工具触发的审批
	Command      string // 什么命令触发的
	WorkDir      string // 命令的工作目录
	TimeoutSec   int    // 多少秒之后这个审批无效
	Reason       string // 风险描述 为什么需要审批
	RiskLevel    string // 风险等级 destructive / write / read_only
	CheckPointID string // Eino Runner checkpoint id
	InterruptID  string // Eino interrupt target id
	ToolCallID   string // 原始 tool call id
	Status       string // pending / approved / rejected / executed / failed
	CreatedAt    time.Time
	ResolvedAt   time.Time
}

type ResumeRequest struct {
	SessionID string
	PendingID string
	Approved  bool
}

// PendingApproval Status 状态取值
const (
	PendingStatusPending  = "pending"
	PendingStatusApproved = "approved"
	PendingStatusRejected = "rejected"
	PendingStatusExecuted = "executed"
	PendingStatusFailed   = "failed"
)
