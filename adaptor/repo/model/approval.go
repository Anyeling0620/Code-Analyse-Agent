package model

import (
	"time"
)

// Approval 是 PendingApproval 的 GORM 持久化层映射。
type Approval struct {
	ID           string    `gorm:"primaryKey;size:64"` // pending_id，贯穿 SSE interrupt 和 resume 请求。
	UserID       string    `gorm:"size:128;index"`     // 审批归属用户。
	SessionID    string    `gorm:"size:128;index"`     // 审批归属会话。
	Tool         string    `gorm:"size:64"`            // 触发审批的工具名，当前为 terminal_exec。
	Command      string    `gorm:"type:text"`          // 待确认的原始命令。
	Workdir      string    `gorm:"type:text"`          // 命令请求工作目录。
	TimeoutSec   int       // 命令请求超时时间。
	Reason       string    `gorm:"type:text"`      // 风险命中原因。
	RiskLevel    string    `gorm:"size:32"`        // 风险等级。
	CheckpointID string    `gorm:"size:256;index"` // Eino Runner checkpoint id。
	InterruptID  string    `gorm:"size:256;index"` // Eino interrupt target id，conversation 层收到中断后回填。
	ToolCallID   string    `gorm:"size:128"`       // 原始工具调用 ID。
	Status       string    `gorm:"size:32;index"`  // pending/approved/rejected/executed/failed。
	CreatedAt    time.Time // 审批创建时间。
	ResolvedAt   time.Time // 审批进入终态或中间态的最近处理时间。
}

// TableName 返回审批记录表名。
func (Approval) TableName() string { return "approvals" }

//
//// NewApproval 把审批 DTO 转成 GORM 模型。
//func NewApproval(p *dto.PendingApproval) *Approval {
//	if p == nil {
//		return nil
//	}
//	return &Approval{
//		ID:           p.ID,
//		UserID:       p.UserID,
//		SessionID:    p.SessionID,
//		Tool:         p.Tool,
//		Command:      p.Command,
//		Workdir:      p.Workdir,
//		TimeoutSec:   p.TimeoutSec,
//		Reason:       p.Reason,
//		RiskLevel:    p.RiskLevel,
//		CheckpointID: p.CheckpointID,
//		InterruptID:  p.InterruptID,
//		ToolCallID:   p.ToolCallID,
//		Status:       p.Status,
//		CreatedAt:    p.CreatedAt,
//		ResolvedAt:   p.ResolvedAt,
//	}
//}
//
//// ToDomain 把 GORM 模型转成审批 DTO。
//func (m *Approval) ToDomain() *dto.PendingApproval {
//	if m == nil {
//		return nil
//	}
//	return &dto.PendingApproval{
//		ID:           m.ID,
//		UserID:       m.UserID,
//		SessionID:    m.SessionID,
//		Tool:         m.Tool,
//		Command:      m.Command,
//		Workdir:      m.Workdir,
//		TimeoutSec:   m.TimeoutSec,
//		Reason:       m.Reason,
//		RiskLevel:    m.RiskLevel,
//		CheckpointID: m.CheckpointID,
//		InterruptID:  m.InterruptID,
//		ToolCallID:   m.ToolCallID,
//		Status:       m.Status,
//		CreatedAt:    m.CreatedAt,
//		ResolvedAt:   m.ResolvedAt,
//	}
//}
