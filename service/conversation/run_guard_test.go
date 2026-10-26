package conversation

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"edu.agent.code/adaptor/repo/model"
	runrepo "edu.agent.code/adaptor/repo/run"
	"edu.agent.code/adaptor/repo/session"
	"edu.agent.code/common"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// guardTestSessions 只提供本轮准备用到的两个读取能力。
// 内嵌 nil 接口保证：一旦 prepareChatRun 将来用到更多会话能力，
// 这里会以明确的 nil panic 暴露，而不是静默返回零值让测试失去意义。
type guardTestSessions struct {
	session.ISession
}

func (guardTestSessions) GetByID(_ context.Context, sessionID string) (*do.SessionContext, error) {
	if sessionID == "" {
		return nil, nil
	}
	return &do.SessionContext{SessionID: sessionID, UserID: "u1", UpdatedAt: time.Now()}, nil
}

func (guardTestSessions) ListMessages(context.Context, string, string, dto.Pager, bool) ([]do.ChatMessageRecord, bool, error) {
	return nil, false, nil
}

// newGuardTestService 构造一个「真实 run 仓储 + 最小会话/画像」的 Service。
// 守卫的判定同时依赖内存占位与仓储兜底，因此必须用真库，不能用空实现。
func newGuardTestService(t *testing.T) (*Service, *runrepo.Run) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "conv_guard_test.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.ChatRun{}, &model.ChatRunEvent{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	repo := runrepo.NewRunWithDB(db)
	svc := &Service{
		sessions: guardTestSessions{},
		profiles: &fakeProfiles{rows: map[string]*do.Profile{}},
		runs:     repo,
	}
	return svc, repo
}

func guardTestRun(runID, sessionID, status string) *do.ChatRun {
	now := time.Now()
	return &do.ChatRun{
		RunID:     runID,
		TraceID:   "trace-" + runID,
		SessionID: sessionID,
		UserID:    "u1",
		Question:  "q",
		Status:    status,
		StartedAt: now,
		UpdatedAt: now,
	}
}

func guardTestRequest(sessionID string) dto.ChatRequest {
	return dto.ChatRequest{UserID: "u1", SessionID: sessionID, Message: "继续分析这个项目"}
}

// requireSessionBusy 校验错误是「同会话已有进行中的回答」，且能被转成 409。
func requireSessionBusy(t *testing.T, err error) *SessionBusyError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected session busy error, got nil")
	}
	if !errors.Is(err, common.ErrSessionBusy) {
		t.Fatalf("error should be identifiable as session busy, got %v", err)
	}
	var busy *SessionBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("error should carry SessionBusyError, got %T", err)
	}
	errno, ok := common.AsErrno(err)
	if !ok || errno.Code != 409 {
		t.Fatalf("session busy should map to 409, got code=%d ok=%v", errno.Code, ok)
	}
	return busy
}

// 同会话已有 running 的 run 时，新请求必须被拒。
func TestPrepareChatRunRejectsWhenSessionAlreadyRunning(t *testing.T) {
	svc, repo := newGuardTestService(t)
	ctx := context.Background()

	if err := repo.Create(ctx, guardTestRun("run-running", "s1", do.RunStatusRunning)); err != nil {
		t.Fatalf("create running run: %v", err)
	}

	_, prep, err := svc.prepareChatRun(ctx, guardTestRequest("s1"))
	busy := requireSessionBusy(t, err)
	if prep != nil {
		t.Fatalf("rejected request must not return a prep, got %+v", prep)
	}
	// 内存守卫为空时由仓储兜底命中，此时能定位到占用的 run。
	if busy.RunID != "run-running" {
		t.Fatalf("busy error should carry occupying run id, got %q", busy.RunID)
	}
	if svc.sessionRuns.isRunning("u1", "s1") {
		t.Fatalf("rejected request must not keep the session occupied")
	}
}

// interrupted 只是等待审批恢复，没有 goroutine 在跑，必须放行。
// 这条用例守住 /api/chat/resume 与「中断后继续提问」的既有路径。
func TestPrepareChatRunAllowsWhenOnlyInterrupted(t *testing.T) {
	svc, repo := newGuardTestService(t)
	ctx := context.Background()

	if err := repo.Create(ctx, guardTestRun("run-interrupted", "s1", do.RunStatusInterrupted)); err != nil {
		t.Fatalf("create interrupted run: %v", err)
	}

	_, prep, err := svc.prepareChatRun(ctx, guardTestRequest("s1"))
	if err != nil {
		t.Fatalf("interrupted run must not block a new turn: %v", err)
	}
	if prep == nil || prep.runID == "" {
		t.Fatalf("expected a prepared run, got %+v", prep)
	}
	// 放行后要落到一条新的 running run，且会话被本轮占用。
	active, err := repo.GetRunningBySession(ctx, "u1", "s1")
	if err != nil {
		t.Fatalf("get running: %v", err)
	}
	if active == nil || active.RunID != prep.runID {
		t.Fatalf("new run should be the running one, got %+v", active)
	}
	if !svc.sessionRuns.isRunning("u1", "s1") {
		t.Fatalf("acquired session should stay occupied until the run finishes")
	}
}

// run 进入终态后，同一会话可以再次发起。
func TestPrepareChatRunAllowsAfterRunFinished(t *testing.T) {
	svc, repo := newGuardTestService(t)
	ctx := context.Background()

	if err := repo.Create(ctx, guardTestRun("run-old", "s1", do.RunStatusRunning)); err != nil {
		t.Fatalf("create running run: %v", err)
	}
	// 先证明它确实是拦的。
	if _, _, err := svc.prepareChatRun(ctx, guardTestRequest("s1")); err == nil {
		t.Fatalf("expected rejection while run-old is running")
	}
	if err := repo.UpdateStatus(ctx, "run-old", do.RunStatusDone, map[string]any{"answer": "ok"}); err != nil {
		t.Fatalf("finish run: %v", err)
	}

	_, prep, err := svc.prepareChatRun(ctx, guardTestRequest("s1"))
	if err != nil {
		t.Fatalf("session should be reusable after run reaches terminal state: %v", err)
	}
	if prep == nil || prep.runID == "run-old" {
		t.Fatalf("expected a fresh run, got %+v", prep)
	}
}

// 并发同会话请求只能有一个成功，其余必须拿到 session busy。
func TestPrepareChatRunConcurrentSameSessionOnlyOneSucceeds(t *testing.T) {
	svc, _ := newGuardTestService(t)
	ctx := context.Background()

	const workers = 8
	var start sync.WaitGroup
	start.Add(1)
	var wg sync.WaitGroup
	results := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			start.Wait() // 让所有 goroutine 尽量同时穿过闸门
			_, _, err := svc.prepareChatRun(ctx, guardTestRequest("s1"))
			results[idx] = err
		}(i)
	}
	start.Done()
	wg.Wait()

	var okCount, busyCount int
	for i, err := range results {
		switch {
		case err == nil:
			okCount++
		case errors.Is(err, common.ErrSessionBusy):
			busyCount++
		default:
			t.Fatalf("worker %d got unexpected error: %v", i, err)
		}
	}
	if okCount != 1 {
		t.Fatalf("exactly one concurrent request may create a run, got %d", okCount)
	}
	if busyCount != workers-1 {
		t.Fatalf("remaining %d requests should be rejected as busy, got %d", workers-1, busyCount)
	}
}

// 不同会话必须能同时创建 run：跨会话并行不能被串行守卫误伤。
func TestPrepareChatRunAllowsDifferentSessionsConcurrently(t *testing.T) {
	svc, _ := newGuardTestService(t)
	ctx := context.Background()

	const workers = 6
	var start sync.WaitGroup
	start.Add(1)
	var wg sync.WaitGroup
	results := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			start.Wait()
			_, _, err := svc.prepareChatRun(ctx, guardTestRequest(fmt.Sprintf("s-%d", idx)))
			results[idx] = err
		}(i)
	}
	start.Done()
	wg.Wait()

	for i, err := range results {
		if err != nil {
			t.Fatalf("session s-%d should be creatable in parallel, got %v", i, err)
		}
	}
	for i := 0; i < workers; i++ {
		if !svc.sessionRuns.isRunning("u1", fmt.Sprintf("s-%d", i)) {
			t.Fatalf("session s-%d should be occupied after its run started", i)
		}
	}
}

// 释放后同一会话能再次发起，保证锁不泄漏。
//
// 同时固定一条不变量：**内存释放必须发生在 run 落成终态之后**。
// 真实链路里 executeRun 先 finishRun(UpdateStatus → done/failed) 收敛 run，
// 后台 goroutine 的 defer 才调 endSessionRun；顺序颠倒会让会话在库里仍是
// running 时就被放行——本用例的前半段正是这个顺序的证据。
func TestSessionRunGuardReleasedAllowsNextTurn(t *testing.T) {
	svc, repo := newGuardTestService(t)
	ctx := context.Background()

	_, prep, err := svc.prepareChatRun(ctx, guardTestRequest("s1"))
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if _, _, err := svc.prepareChatRun(ctx, guardTestRequest("s1")); err == nil {
		t.Fatalf("second concurrent turn should be rejected")
	}

	// 只释放内存、run 仍是 running：仓储兜底必须继续拦，否则并发保护形同虚设。
	svc.endSessionRun("u1", "s1")
	if svc.sessionRuns.isRunning("u1", "s1") {
		t.Fatalf("session should be free in memory after release")
	}
	if _, _, err := svc.prepareChatRun(ctx, guardTestRequest("s1")); !errors.Is(err, common.ErrSessionBusy) {
		t.Fatalf("running row must still block even after in-memory release, got %v", err)
	}

	// 走真实收敛路径：先落终态，再释放。此后同一会话可再次发起。
	if err := repo.UpdateStatus(ctx, prep.runID, do.RunStatusDone, map[string]any{"answer": "ok"}); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	svc.endSessionRun("u1", "s1")
	if _, _, err := svc.prepareChatRun(ctx, guardTestRequest("s1")); err != nil {
		t.Fatalf("session should be reusable after run reached terminal state: %v", err)
	}
}

// 不同用户的同名 session_id 不能互相阻塞。
func TestSessionRunGuardIsolatesUsers(t *testing.T) {
	svc, _ := newGuardTestService(t)
	ctx := context.Background()

	if err := svc.beginSessionRun(ctx, "u1", "shared-id"); err != nil {
		t.Fatalf("user u1 acquire: %v", err)
	}
	if err := svc.beginSessionRun(ctx, "u2", "shared-id"); err != nil {
		t.Fatalf("another user with the same session id must not be blocked: %v", err)
	}
	if err := svc.beginSessionRun(ctx, "u1", "shared-id"); err == nil {
		t.Fatalf("same user + same session must be blocked")
	}
}
