package run

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/model"
	"edu.agent.code/service/do"
	"errors"
	"gorm.io/gorm"
	"time"
)

// IRun 是 run 状态机与 run 事件流的仓储。
//
// 两张表的分工：
//   - chat_runs 记录一次执行的生命周期（状态、起止时间、降级标记、项目上下文）；
//   - chat_run_events 记录执行过程中产生的每一条可回放事件，id 即 SSE 游标。
//
// 事件表用自增主键当游标：客户端拿到的是单调递增的 seq，回放时按
// (run_id, id > after) 过滤即可，不需要额外维护序号列。
type IRun interface {
	Create(ctx context.Context, item *do.ChatRun) error
	GetByID(ctx context.Context, runID string) (*do.ChatRun, error)
	GetByCheckpointID(ctx context.Context, checkpointID string) (*do.ChatRun, error)
	// GetActiveBySession 取该用户该会话下最近一条仍可续跑的 run（running / interrupted）。
	GetActiveBySession(ctx context.Context, userID, sessionID string) (*do.ChatRun, error)
	UpdateStatus(ctx context.Context, runID, status string, extra map[string]any) error
	// AppendEvent 落库一条事件并返回它分配到的 seq。
	AppendEvent(ctx context.Context, runID, sessionID, userID, eventType string, payload []byte) (int64, error)
	ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]do.ChatRunEvent, error)
	// MarkRunningAsInterrupted 是进程启动扫描：把上次进程留下的 running 全部标成 interrupted。
	MarkRunningAsInterrupted(ctx context.Context) (int64, error)
}

type Run struct {
	db *gorm.DB
}

func NewRun(a adaptor.IAdaptor) *Run {
	return &Run{db: a.GetDB()}
}

// NewRunWithDB 用现成的 *gorm.DB 构造仓储，供单测复用。
func NewRunWithDB(db *gorm.DB) *Run {
	return &Run{db: db}
}

func (r *Run) Create(ctx context.Context, item *do.ChatRun) error {
	if item == nil {
		return errors.New("nil run")
	}
	row := model.ChatRun{
		RunID:             item.RunID,
		TraceID:           item.TraceID,
		SessionID:         item.SessionID,
		UserID:            item.UserID,
		Question:          item.Question,
		Answer:            item.Answer,
		Status:            item.Status,
		Degraded:          item.Degraded,
		ErrorMsg:          item.ErrorMsg,
		CheckpointID:      item.CheckpointID,
		PendingApprovalID: item.PendingApprovalID,
		ProjectRoot:       item.ProjectRoot,
		ProjectName:       item.ProjectName,
		LastSeq:           item.LastSeq,
		StartedAt:         item.StartedAt,
		UpdatedAt:         item.UpdatedAt,
		EndedAt:           item.EndedAt,
	}
	return r.db.WithContext(ctx).Create(&row).Error
}

func (r *Run) GetByID(ctx context.Context, runID string) (*do.ChatRun, error) {
	if runID == "" {
		return nil, nil
	}
	var row model.ChatRun
	err := r.db.WithContext(ctx).Where("run_id = ?", runID).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return toDomain(&row), nil
}

func (r *Run) GetByCheckpointID(ctx context.Context, checkpointID string) (*do.ChatRun, error) {
	if checkpointID == "" {
		return nil, nil
	}
	var row model.ChatRun
	err := r.db.WithContext(ctx).
		Where("checkpoint_id = ?", checkpointID).
		Order("started_at DESC").
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return toDomain(&row), nil
}

func (r *Run) GetActiveBySession(ctx context.Context, userID, sessionID string) (*do.ChatRun, error) {
	if userID == "" || sessionID == "" {
		return nil, nil
	}
	var row model.ChatRun
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND session_id = ? AND status IN ?", userID, sessionID,
			[]string{do.RunStatusRunning, do.RunStatusInterrupted}).
		Order("started_at DESC, run_id DESC").
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return toDomain(&row), nil
}

// UpdateStatus 更新 run 状态，extra 用于同时回填 answer / degraded / error_msg /
// pending_approval_id / project_root / ended_at 等字段。
func (r *Run) UpdateStatus(ctx context.Context, runID, status string, extra map[string]any) error {
	if runID == "" {
		return nil
	}
	updates := map[string]any{
		"status":     status,
		"updated_at": time.Now(),
	}
	for key, value := range extra {
		updates[key] = value
	}
	return r.db.WithContext(ctx).
		Model(&model.ChatRun{}).
		Where("run_id = ?", runID).
		Updates(updates).Error
}

// AppendEvent 在同一个事务里写事件并回填 chat_runs.last_seq，
// 保证「事件已落库」与「run 的游标已推进」不会出现中间态。
func (r *Run) AppendEvent(ctx context.Context, runID, sessionID, userID, eventType string, payload []byte) (int64, error) {
	if runID == "" {
		return 0, errors.New("empty run id")
	}
	var seq int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := model.ChatRunEvent{
			RunID:     runID,
			SessionID: sessionID,
			UserID:    userID,
			Type:      eventType,
			Payload:   string(payload),
			CreatedAt: time.Now(),
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		seq = row.ID
		return tx.Model(&model.ChatRun{}).
			Where("run_id = ?", runID).
			Updates(map[string]any{"last_seq": seq, "updated_at": time.Now()}).Error
	})
	if err != nil {
		return 0, err
	}
	return seq, nil
}

func (r *Run) ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]do.ChatRunEvent, error) {
	if runID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultEventReplayLimit
	}
	if limit > maxEventReplayLimit {
		limit = maxEventReplayLimit
	}
	var rows []*model.ChatRunEvent
	err := r.db.WithContext(ctx).
		Where("run_id = ? AND id > ?", runID, afterSeq).
		Order("id ASC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	events := make([]do.ChatRunEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, do.ChatRunEvent{
			Seq:       row.ID,
			RunID:     row.RunID,
			SessionID: row.SessionID,
			UserID:    row.UserID,
			Type:      row.Type,
			Payload:   []byte(row.Payload),
			CreatedAt: row.CreatedAt,
		})
	}
	return events, nil
}

func (r *Run) MarkRunningAsInterrupted(ctx context.Context) (int64, error) {
	res := r.db.WithContext(ctx).
		Model(&model.ChatRun{}).
		Where("status = ?", do.RunStatusRunning).
		Updates(map[string]any{
			"status":     do.RunStatusInterrupted,
			"updated_at": time.Now(),
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

const (
	defaultEventReplayLimit = 1000
	maxEventReplayLimit     = 5000
)

func toDomain(row *model.ChatRun) *do.ChatRun {
	if row == nil {
		return nil
	}
	return &do.ChatRun{
		RunID:             row.RunID,
		TraceID:           row.TraceID,
		SessionID:         row.SessionID,
		UserID:            row.UserID,
		Question:          row.Question,
		Answer:            row.Answer,
		Status:            row.Status,
		Degraded:          row.Degraded,
		ErrorMsg:          row.ErrorMsg,
		CheckpointID:      row.CheckpointID,
		PendingApprovalID: row.PendingApprovalID,
		ProjectRoot:       row.ProjectRoot,
		ProjectName:       row.ProjectName,
		LastSeq:           row.LastSeq,
		StartedAt:         row.StartedAt,
		UpdatedAt:         row.UpdatedAt,
		EndedAt:           row.EndedAt,
	}
}
