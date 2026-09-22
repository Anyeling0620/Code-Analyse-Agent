package approval

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/model"
	"edu.agent.code/service/do"
	"errors"
	"gorm.io/gorm"
	"time"
)

type IApproval interface {
	Insert(ctx context.Context, req *do.PendingApproval) error
	GetByID(ctx context.Context, id string) (*do.PendingApproval, error)
	BindInterrupt(ctx context.Context, pendingID, checkpointID, interruptID string) error
	Resolve(ctx context.Context, pendingID, checkpointID string) error
}

type Approval struct {
	db *gorm.DB
}

func NewApproval(adaptor adaptor.IAdaptor) *Approval {
	return &Approval{
		db: adaptor.GetDB(),
	}
}

func (a *Approval) Insert(ctx context.Context, req *do.PendingApproval) error {
	if req == nil {
		return errors.New("nil request")
	}
	row := model.Approval{
		UserID:       req.UserID,
		SessionID:    req.SessionID,
		Tool:         req.Tool,
		Command:      req.Command,
		Workdir:      req.WorkDir,
		TimeoutSec:   req.TimeoutSec,
		Reason:       req.Reason,
		RiskLevel:    req.RiskLevel,
		CheckpointID: req.CheckPointID,
		InterruptID:  req.InterruptID,
		ToolCallID:   req.ToolCallID,
		Status:       req.Status,
		CreatedAt:    req.CreatedAt,
		ResolvedAt:   req.ResolvedAt,
	}
	return a.db.WithContext(ctx).Create(&row).Error
}

func (a *Approval) GetByID(ctx context.Context, pendingId string) (*do.PendingApproval, error) {
	var row model.Approval
	err := a.db.WithContext(ctx).First(&row, "pending_id = ?", pendingId).Error
	if err != nil {
		return nil, err
	}
	return &do.PendingApproval{
		ID:           row.ID,
		UserID:       row.UserID,
		SessionID:    row.SessionID,
		Tool:         row.Tool,
		Command:      row.Command,
		WorkDir:      row.Workdir,
		TimeoutSec:   row.TimeoutSec,
		Reason:       row.Reason,
		RiskLevel:    row.RiskLevel,
		CheckPointID: row.CheckpointID,
		InterruptID:  row.InterruptID,
		ToolCallID:   row.ToolCallID,
		Status:       row.Status,
		CreatedAt:    row.CreatedAt,
		ResolvedAt:   row.ResolvedAt,
	}, nil
}

func (a *Approval) BindInterrupt(ctx context.Context, pendingID, checkpointID, interruptID string) error {
	updates := map[string]interface{}{
		"checkpoint_id": checkpointID,
		"interrupt_id":  interruptID,
	}
	return a.db.WithContext(ctx).Model(&model.Approval{}).Where("id = ?", pendingID).Updates(updates).Error
}

func (a *Approval) Resolve(ctx context.Context, pendingID, status string) error {
	updates := map[string]interface{}{
		"status":      status,
		"resolved_at": time.Now(),
	}
	return a.db.WithContext(ctx).Model(&model.Approval{}).Where("id = ?", pendingID).Updates(updates).Error
}
