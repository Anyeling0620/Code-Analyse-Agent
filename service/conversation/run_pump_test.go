package conversation

import (
	"context"
	"edu.agent.code/adaptor/repo/model"
	runrepo "edu.agent.code/adaptor/repo/run"
	"edu.agent.code/service/consts"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"
	"errors"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// newTestService 构造一个只带 run 仓储与 broker 的 Service。
// 这些用例只验证"事件落库 + 广播 + 回放"这条链路，不需要模型/工具依赖。
func newTestService(t *testing.T) (*Service, *runrepo.Run) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "conv_run_test.db")), &gorm.Config{
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
	repo := runrepo.NewRunWithDB(db)
	return &Service{runs: repo, broker: newRunBroker()}, repo
}

func testRun(runID, status string) *do.ChatRun {
	now := time.Now()
	return &do.ChatRun{
		RunID:     runID,
		TraceID:   "trace-" + runID,
		SessionID: "session-1",
		UserID:    "user-1",
		Question:  "question",
		Status:    status,
		StartedAt: now,
		UpdatedAt: now,
	}
}

func testRunState(runID string) *dto.ChatRunState {
	return &dto.ChatRunState{
		UserID:      "user-1",
		RunID:       runID,
		TraceID:     "trace-" + runID,
		SessionID:   "session-1",
		ToolCallMap: map[string]dto.ToolCallState{},
	}
}

func mustPublish(t *testing.T, svc *Service, ctx context.Context, runState *dto.ChatRunState, eventType string) dto.ChatStreamEvent {
	t.Helper()
	if err := svc.publishRunEvent(ctx, runState, dto.ChatStreamEvent{Type: eventType}); err != nil {
		t.Fatalf("publish %s: %v", eventType, err)
	}
	return dto.ChatStreamEvent{Type: eventType, Seq: runState.LastSeq, RunID: runState.RunID}
}

func TestPublishRunEventPersistsAndBroadcasts(t *testing.T) {
	svc, repo := newTestService(t)
	ctx := context.Background()
	if err := repo.Create(ctx, testRun("run-1", do.RunStatusRunning)); err != nil {
		t.Fatalf("create run: %v", err)
	}

	live, cancel := svc.broker.Subscribe("run-1")
	defer cancel()

	runState := testRunState("run-1")
	if err := svc.publishRunEvent(ctx, runState, dto.ChatStreamEvent{Type: "delta", Delta: "hello"}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if runState.LastSeq == 0 {
		t.Fatal("expected run state last_seq to be回填")
	}

	select {
	case event := <-live:
		if event.Seq != runState.LastSeq {
			t.Fatalf("live seq=%d want %d", event.Seq, runState.LastSeq)
		}
		if event.RunID != "run-1" || event.Delta != "hello" {
			t.Fatalf("unexpected live event: %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live subscriber did not receive the event")
	}

	events, err := repo.ListEventsAfter(ctx, "run-1", 0, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 || events[0].Seq != runState.LastSeq || events[0].Type != "delta" {
		t.Fatalf("unexpected persisted events: %+v", events)
	}
	// 落库的 payload 里必须带上 run_id / seq，重连回放才能直接还原事件。
	decoded, err := decodeRunEvent(events[0])
	if err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if decoded.RunID != "run-1" || decoded.Seq != runState.LastSeq || decoded.Delta != "hello" {
		t.Fatalf("unexpected decoded event: %+v", decoded)
	}
}

// TestPumpRunReplaysPersistedThenFollowsLive 覆盖最典型的断连续传场景：
// 客户端错过的事件已经落库，重连后要把"历史 + 实时"无缝拼起来且不重复。
func TestPumpRunReplaysPersistedThenFollowsLive(t *testing.T) {
	svc, repo := newTestService(t)
	ctx := context.Background()
	if err := repo.Create(ctx, testRun("run-1", do.RunStatusRunning)); err != nil {
		t.Fatalf("create run: %v", err)
	}

	runState := testRunState("run-1")
	// 订阅之前就已经产生（并落库）的事件。
	mustPublish(t, svc, ctx, runState, "session")
	mustPublish(t, svc, ctx, runState, "delta")

	var mu sync.Mutex
	got := make([]int64, 0, 3)
	pumpDone := make(chan error, 1)
	go func() {
		pumpDone <- svc.PumpRun(context.Background(), "run-1", 0, func(event dto.ChatStreamEvent) error {
			mu.Lock()
			got = append(got, event.Seq)
			mu.Unlock()
			return nil
		})
	}()

	// 等订阅生效后再发实时事件，模拟"断线期间执行仍在推进"。
	time.Sleep(100 * time.Millisecond)
	mustPublish(t, svc, ctx, runState, "done")
	svc.broker.Close("run-1")

	select {
	case err := <-pumpDone:
		if err != nil {
			t.Fatalf("pump run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pump run did not finish")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("expected 3 events (2 replayed + 1 live), got %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("events duplicated or out of order: %v", got)
		}
	}
}

// TestPumpRunAfterSeqOnlyReplaysTail 验证 after 游标语义：客户端只补缺失部分。
func TestPumpRunAfterSeqOnlyReplaysTail(t *testing.T) {
	svc, repo := newTestService(t)
	ctx := context.Background()
	if err := repo.Create(ctx, testRun("run-1", do.RunStatusDone)); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runState := testRunState("run-1")
	first := mustPublish(t, svc, ctx, runState, "session")
	mustPublish(t, svc, ctx, runState, "delta")
	last := mustPublish(t, svc, ctx, runState, "done")

	var got []int64
	if err := svc.PumpRun(context.Background(), "run-1", first.Seq, func(event dto.ChatStreamEvent) error {
		got = append(got, event.Seq)
		return nil
	}); err != nil {
		t.Fatalf("pump run: %v", err)
	}
	if len(got) != 2 || got[0] <= first.Seq || got[len(got)-1] != last.Seq {
		t.Fatalf("unexpected replayed events: %v (first=%d last=%d)", got, first.Seq, last.Seq)
	}
}

// TestPumpRunClientDisconnectDoesNotAffectRun 是本次改造的核心断言：
// 客户端断开只会让转发结束，run 本身继续处于 running 且事件照常落库。
func TestPumpRunClientDisconnectDoesNotAffectRun(t *testing.T) {
	svc, repo := newTestService(t)
	ctx := context.Background()
	if err := repo.Create(ctx, testRun("run-1", do.RunStatusRunning)); err != nil {
		t.Fatalf("create run: %v", err)
	}

	pumpCtx, cancel := context.WithCancel(context.Background())
	pumpDone := make(chan error, 1)
	go func() {
		pumpDone <- svc.PumpRun(pumpCtx, "run-1", 0, func(event dto.ChatStreamEvent) error {
			return nil
		})
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-pumpDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pump run did not return after client disconnect")
	}

	item, err := repo.GetByID(ctx, "run-1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if item.Status != do.RunStatusRunning {
		t.Fatalf("client disconnect changed run status to %s", item.Status)
	}

	// 断开之后执行仍然在推进：事件照常落库，重连能补齐。
	runState := testRunState("run-1")
	mustPublish(t, svc, ctx, runState, "delta")
	events, err := repo.ListEventsAfter(ctx, "run-1", 0, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events after client disconnect were lost: %+v", events)
	}
}

// TestPumpRunSynthesizesErrorForInterruptedRun 覆盖进程重启留下的 run：
// 没有任何终止事件，回放完必须补一条 error，避免客户端一直等。
func TestPumpRunSynthesizesErrorForInterruptedRun(t *testing.T) {
	svc, repo := newTestService(t)
	ctx := context.Background()
	if err := repo.Create(ctx, testRun("run-1", do.RunStatusInterrupted)); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runState := testRunState("run-1")
	mustPublish(t, svc, ctx, runState, "session")

	var got []dto.ChatStreamEvent
	if err := svc.PumpRun(context.Background(), "run-1", 0, func(event dto.ChatStreamEvent) error {
		got = append(got, event)
		return nil
	}); err != nil {
		t.Fatalf("pump run: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected replayed session + synthetic error, got %+v", got)
	}
	if got[1].Type != consts.SseEventTypeError {
		t.Fatalf("expected synthetic error event, got %s", got[1].Type)
	}
	if got[1].Seq != 0 {
		t.Fatalf("synthetic error must not carry a seq (it is not persisted), got %d", got[1].Seq)
	}
}

// TestPumpRunReplaysFullStreamAcrossInterrupt 回归：审批中断不是终态。
//
// 中断后 run 还会继续产出"暂停占位 tool_result"和收尾 done，回放必须在
// interrupt 处继续读下去，否则客户端永远等不到 done（而且会误判成断流重连）。
func TestPumpRunReplaysFullStreamAcrossInterrupt(t *testing.T) {
	svc, repo := newTestService(t)
	ctx := context.Background()
	if err := repo.Create(ctx, testRun("run-1", do.RunStatusInterrupted)); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runState := testRunState("run-1")
	mustPublish(t, svc, ctx, runState, "session")
	mustPublish(t, svc, ctx, runState, consts.SseEventTypeInterrupt)
	mustPublish(t, svc, ctx, runState, consts.SseEventTypeToolResult)
	mustPublish(t, svc, ctx, runState, consts.SseEventTypeDone)

	var got []string
	if err := svc.PumpRun(context.Background(), "run-1", 0, func(event dto.ChatStreamEvent) error {
		got = append(got, event.Type)
		return nil
	}); err != nil {
		t.Fatalf("pump run: %v", err)
	}
	want := []string{"session", consts.SseEventTypeInterrupt, consts.SseEventTypeToolResult, consts.SseEventTypeDone}
	if len(got) != len(want) {
		t.Fatalf("expected full stream %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected full stream %v, got %v", want, got)
		}
	}
}

// TestPumpRunTerminalRunDoesNotSynthesizeError 正常跑完的 run 不应被补 error。
func TestPumpRunTerminalRunDoesNotSynthesizeError(t *testing.T) {
	svc, repo := newTestService(t)
	ctx := context.Background()
	if err := repo.Create(ctx, testRun("run-1", do.RunStatusDone)); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runState := testRunState("run-1")
	mustPublish(t, svc, ctx, runState, "session")
	mustPublish(t, svc, ctx, runState, "done")

	var got []string
	if err := svc.PumpRun(context.Background(), "run-1", 0, func(event dto.ChatStreamEvent) error {
		got = append(got, event.Type)
		return nil
	}); err != nil {
		t.Fatalf("pump run: %v", err)
	}
	if len(got) != 2 || got[1] != "done" {
		t.Fatalf("unexpected events: %v", got)
	}
}

func TestRunBrokerSubscribeAfterClose(t *testing.T) {
	broker := newRunBroker()
	live, cancel := broker.Subscribe("run-1")
	defer cancel()

	broker.Publish("run-1", dto.ChatStreamEvent{Type: "delta", Seq: 1})
	select {
	case event := <-live:
		if event.Seq != 1 {
			t.Fatalf("unexpected event: %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber did not receive published event")
	}

	broker.Close("run-1")
	select {
	case _, ok := <-live:
		if ok {
			t.Fatal("subscriber channel should be closed after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber channel not closed")
	}

	// 关闭之后再订阅：必须立刻拿到已关闭的 channel，而不是一直等。
	late, lateCancel := broker.Subscribe("run-1")
	defer lateCancel()
	select {
	case _, ok := <-late:
		if ok {
			t.Fatal("late subscriber should see a closed channel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late subscriber blocked on a closed run")
	}

	// 关闭后的事件推送应当被忽略，不影响后续订阅者。
	broker.Publish("run-1", dto.ChatStreamEvent{Type: "delta", Seq: 2})
}
