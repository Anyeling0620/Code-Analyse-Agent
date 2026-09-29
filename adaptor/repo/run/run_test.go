package run

import (
	"context"
	"edu.agent.code/adaptor/repo/model"
	"edu.agent.code/service/do"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"path/filepath"
	"testing"
	"time"
)

// newRunRepoTestDB 建一个临时 sqlite 库。
// Windows 下必须显式关连接池，否则 t.TempDir() 的清理会因为文件占用失败。
func newRunRepoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "run_test.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})
	if err := db.AutoMigrate(&model.ChatRun{}, &model.ChatRunEvent{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func sampleRun(runID, userID, sessionID, status string) *do.ChatRun {
	now := time.Now()
	return &do.ChatRun{
		RunID:     runID,
		TraceID:   "trace-" + runID,
		SessionID: sessionID,
		UserID:    userID,
		Question:  "q-" + runID,
		Status:    status,
		StartedAt: now,
		UpdatedAt: now,
	}
}

func TestCreateAndGetRun(t *testing.T) {
	ctx := context.Background()
	repo := NewRunWithDB(newRunRepoTestDB(t))

	if err := repo.Create(ctx, sampleRun("run-1", "user-1", "session-1", do.RunStatusRunning)); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := repo.GetByID(ctx, "run-1")
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got == nil || got.RunID != "run-1" || got.Status != do.RunStatusRunning {
		t.Fatalf("unexpected run: %+v", got)
	}

	// 活跃 run 应该能被会话维度查到。
	active, err := repo.GetActiveBySession(ctx, "user-1", "session-1")
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	if active == nil || active.RunID != "run-1" {
		t.Fatalf("expected active run run-1, got %+v", active)
	}

	// 别的用户查不到。
	other, err := repo.GetActiveBySession(ctx, "user-2", "session-1")
	if err != nil {
		t.Fatalf("get active for other user: %v", err)
	}
	if other != nil {
		t.Fatalf("expected nil for other user, got %+v", other)
	}

	// 终态之后不再算活跃。
	if err := repo.UpdateStatus(ctx, "run-1", do.RunStatusDone, map[string]any{"answer": "ok"}); err != nil {
		t.Fatalf("update status: %v", err)
	}
	active, err = repo.GetActiveBySession(ctx, "user-1", "session-1")
	if err != nil {
		t.Fatalf("get active after done: %v", err)
	}
	if active != nil {
		t.Fatalf("expected no active run after done, got %+v", active)
	}
	got, err = repo.GetByID(ctx, "run-1")
	if err != nil {
		t.Fatalf("get by id after done: %v", err)
	}
	if got.Status != do.RunStatusDone || got.Answer != "ok" {
		t.Fatalf("unexpected terminal run: %+v", got)
	}
}

func TestGetByCheckpointID(t *testing.T) {
	ctx := context.Background()
	repo := NewRunWithDB(newRunRepoTestDB(t))

	item := sampleRun("run-1", "user-1", "session-1", do.RunStatusInterrupted)
	item.CheckpointID = "cp-1"
	if err := repo.Create(ctx, item); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := repo.GetByCheckpointID(ctx, "cp-1")
	if err != nil {
		t.Fatalf("get by checkpoint: %v", err)
	}
	if got == nil || got.RunID != "run-1" {
		t.Fatalf("unexpected run for checkpoint: %+v", got)
	}
	miss, err := repo.GetByCheckpointID(ctx, "cp-missing")
	if err != nil {
		t.Fatalf("get by missing checkpoint: %v", err)
	}
	if miss != nil {
		t.Fatalf("expected nil for missing checkpoint, got %+v", miss)
	}
}

func TestAppendEventAssignsMonotonicSeq(t *testing.T) {
	ctx := context.Background()
	repo := NewRunWithDB(newRunRepoTestDB(t))
	if err := repo.Create(ctx, sampleRun("run-1", "user-1", "session-1", do.RunStatusRunning)); err != nil {
		t.Fatalf("create: %v", err)
	}

	var seqs []int64
	for _, eventType := range []string{"session", "delta", "done"} {
		seq, err := repo.AppendEvent(ctx, "run-1", "session-1", "user-1", eventType, []byte(`{"type":"`+eventType+`"}`))
		if err != nil {
			t.Fatalf("append %s: %v", eventType, err)
		}
		seqs = append(seqs, seq)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("seq not strictly increasing: %v", seqs)
		}
	}

	item, err := repo.GetByID(ctx, "run-1")
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if item.LastSeq != seqs[len(seqs)-1] {
		t.Fatalf("last_seq=%d want %d", item.LastSeq, seqs[len(seqs)-1])
	}

	all, err := repo.ListEventsAfter(ctx, "run-1", 0, 0)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 events, got %d", len(all))
	}
	if all[0].Type != "session" || all[2].Type != "done" {
		t.Fatalf("events out of order: %+v", all)
	}

	// after 语义：严格大于游标。
	rest, err := repo.ListEventsAfter(ctx, "run-1", seqs[0], 0)
	if err != nil {
		t.Fatalf("list after first: %v", err)
	}
	if len(rest) != 2 || rest[0].Seq <= seqs[0] {
		t.Fatalf("unexpected events after cursor: %+v", rest)
	}

	// after = 最后一条时不应再返回任何事件。
	tail, err := repo.ListEventsAfter(ctx, "run-1", seqs[len(seqs)-1], 0)
	if err != nil {
		t.Fatalf("list after last: %v", err)
	}
	if len(tail) != 0 {
		t.Fatalf("expected no events after last seq, got %+v", tail)
	}
}

func TestListEventsAfterIsolatesRuns(t *testing.T) {
	ctx := context.Background()
	repo := NewRunWithDB(newRunRepoTestDB(t))
	if err := repo.Create(ctx, sampleRun("run-1", "user-1", "session-1", do.RunStatusRunning)); err != nil {
		t.Fatalf("create run-1: %v", err)
	}
	if err := repo.Create(ctx, sampleRun("run-2", "user-1", "session-1", do.RunStatusRunning)); err != nil {
		t.Fatalf("create run-2: %v", err)
	}
	if _, err := repo.AppendEvent(ctx, "run-1", "session-1", "user-1", "delta", []byte(`{"type":"delta"}`)); err != nil {
		t.Fatalf("append run-1: %v", err)
	}
	if _, err := repo.AppendEvent(ctx, "run-2", "session-1", "user-1", "delta", []byte(`{"type":"delta"}`)); err != nil {
		t.Fatalf("append run-2: %v", err)
	}

	events, err := repo.ListEventsAfter(ctx, "run-1", 0, 0)
	if err != nil {
		t.Fatalf("list run-1: %v", err)
	}
	if len(events) != 1 || events[0].RunID != "run-1" {
		t.Fatalf("run events leaked across runs: %+v", events)
	}
}

func TestMarkRunningAsInterrupted(t *testing.T) {
	ctx := context.Background()
	repo := NewRunWithDB(newRunRepoTestDB(t))

	for _, item := range []*do.ChatRun{
		sampleRun("run-1", "user-1", "session-1", do.RunStatusRunning),
		sampleRun("run-2", "user-2", "session-2", do.RunStatusRunning),
		sampleRun("run-3", "user-3", "session-3", do.RunStatusDone),
		sampleRun("run-4", "user-4", "session-4", do.RunStatusInterrupted),
	} {
		if err := repo.Create(ctx, item); err != nil {
			t.Fatalf("create %s: %v", item.RunID, err)
		}
	}

	affected, err := repo.MarkRunningAsInterrupted(ctx)
	if err != nil {
		t.Fatalf("mark running as interrupted: %v", err)
	}
	if affected != 2 {
		t.Fatalf("expected 2 affected rows, got %d", affected)
	}

	for _, runID := range []string{"run-1", "run-2"} {
		item, err := repo.GetByID(ctx, runID)
		if err != nil {
			t.Fatalf("get %s: %v", runID, err)
		}
		if item.Status != do.RunStatusInterrupted {
			t.Fatalf("%s status=%s want interrupted", runID, item.Status)
		}
	}
	// 幂等：再跑一次不会改动任何行。
	affected, err = repo.MarkRunningAsInterrupted(ctx)
	if err != nil {
		t.Fatalf("second mark: %v", err)
	}
	if affected != 0 {
		t.Fatalf("expected 0 affected rows on second run, got %d", affected)
	}
	// done 不受影响。
	done, err := repo.GetByID(ctx, "run-3")
	if err != nil {
		t.Fatalf("get run-3: %v", err)
	}
	if done.Status != do.RunStatusDone {
		t.Fatalf("run-3 status changed to %s", done.Status)
	}
}
