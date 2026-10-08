package telemetry

import (
	"context"
	"database/sql"
	"edu.agent.code/config"
	"edu.agent.code/utils/logger"
	"sync"
	"sync/atomic"
	"time"

	// mysql 驱动：本包用 database/sql 直连 agent_telemetry 库写埋点。
	// 它与 database_report 只读连接用的是同一个驱动，但**账号不同**，
	// 写账号只有该库的 INSERT/SELECT/UPDATE，碰不到业务库。
	_ "github.com/go-sql-driver/mysql"
)

const (
	defaultQueueSize       = 256
	defaultWriteTimeoutSec = 3
)

// upsertRunMetrics 用 upsert 而不是裸 INSERT：一次审批中断 + resume 会走两遍
// finishRun（interrupted 一次、done 一次），同一个 run_id 必须收敛成一行，
// 且后写的终态覆盖先写的。
//
// 注意 ON DUPLICATE KEY UPDATE 需要 UPDATE 权限——写账号的授权里必须包含它，
// 只给 SELECT/INSERT 会在运行时直接报 ERROR 1142。
const upsertRunMetrics = `
INSERT INTO run_metrics (
  run_id, trace_id, session_id, user_id, status, degraded, degraded_reason,
  llm_calls, max_iterations, max_iterations_hit,
  tool_calls, tool_errors, distinct_tools, compactions,
  prompt_tokens, cached_tokens, completion_tokens, total_tokens, cost_cny,
  duration_ms, project_name, started_at, ended_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON DUPLICATE KEY UPDATE
  trace_id           = VALUES(trace_id),
  session_id         = VALUES(session_id),
  user_id            = VALUES(user_id),
  status             = VALUES(status),
  degraded           = VALUES(degraded),
  degraded_reason    = VALUES(degraded_reason),
  llm_calls          = VALUES(llm_calls),
  max_iterations     = VALUES(max_iterations),
  max_iterations_hit = VALUES(max_iterations_hit),
  tool_calls         = VALUES(tool_calls),
  tool_errors        = VALUES(tool_errors),
  distinct_tools     = VALUES(distinct_tools),
  compactions        = VALUES(compactions),
  prompt_tokens      = VALUES(prompt_tokens),
  cached_tokens      = VALUES(cached_tokens),
  completion_tokens  = VALUES(completion_tokens),
  total_tokens       = VALUES(total_tokens),
  cost_cny           = VALUES(cost_cny),
  duration_ms        = VALUES(duration_ms),
  project_name       = VALUES(project_name),
  ended_at           = VALUES(ended_at)`

// Writer 把 run 级聚合指标异步写入 agent_telemetry.run_metrics。
//
// 设计前提：**埋点绝不能影响对话主流程**。投递是非阻塞的，队列满时直接丢弃并计数；
// 写入用独立的 context.Background()，不挂 run 的 ctx——run 结束时它已经被取消，
// 挂上去会导致所有终态埋点集体写失败。
type Writer struct {
	db      *sql.DB
	queue   chan RunMetrics
	timeout time.Duration

	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	dropped atomic.Int64 // 队列满被丢弃
	written atomic.Int64 // 成功落库
	failed  atomic.Int64 // 写库失败
}

// NewWriter 按配置建立埋点写入器。enable 为 false 或 dsn 为空时返回 (nil, nil)，
// 调用方按"埋点关闭"处理，不需要判错。
func NewWriter(conf config.Telemetry) (*Writer, error) {
	if !conf.Enable || conf.DSN == "" {
		return nil, nil
	}
	queueSize := conf.QueueSize
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	timeoutSec := conf.WriteTimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = defaultWriteTimeoutSec
	}
	db, err := sql.Open("mysql", conf.DSN)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(30 * time.Minute)

	w := &Writer{
		db:      db,
		queue:   make(chan RunMetrics, queueSize),
		timeout: time.Duration(timeoutSec) * time.Second,
		done:    make(chan struct{}),
	}
	w.wg.Add(1)
	go w.loop()
	return w, nil
}

// Write 非阻塞投递一条指标。队列满时丢弃并累加 dropped 计数。
func (w *Writer) Write(m RunMetrics) {
	if w == nil {
		return
	}
	select {
	case w.queue <- m:
	default:
		n := w.dropped.Add(1)
		// 只在第 1 次和每 64 次记一条，避免队列持续打满时把日志刷爆。
		if n == 1 || n%64 == 0 {
			logger.Warn("telemetry: queue full, metrics dropped run_id=%s dropped_total=%d", m.RunID, n)
		}
	}
}

// Stats 返回 dropped / written / failed 三个计数，供健康检查与日志使用。
func (w *Writer) Stats() (dropped, written, failed int64) {
	if w == nil {
		return 0, 0, 0
	}
	return w.dropped.Load(), w.written.Load(), w.failed.Load()
}

// Close 停止后台 goroutine 并等它把队列里剩下的指标尽量写完，然后关闭连接。
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.stopOnce.Do(func() { close(w.done) })
	w.wg.Wait()
	return w.db.Close()
}

func (w *Writer) loop() {
	defer w.wg.Done()
	for {
		select {
		case m := <-w.queue:
			w.persist(m)
		case <-w.done:
			w.drain()
			dropped, written, failed := w.Stats()
			logger.Info("telemetry: writer stopped dropped=%d written=%d failed=%d", dropped, written, failed)
			return
		}
	}
}

// drain 在退出前排空队列：进程正常关停时不该丢掉已经投递的指标。
func (w *Writer) drain() {
	for {
		select {
		case m := <-w.queue:
			w.persist(m)
		default:
			return
		}
	}
}

func (w *Writer) persist(m RunMetrics) {
	ctx, cancel := context.WithTimeout(context.Background(), w.timeout)
	defer cancel()

	var endedAt any
	if !m.EndedAt.IsZero() {
		endedAt = m.EndedAt
	}
	_, err := w.db.ExecContext(ctx, upsertRunMetrics,
		m.RunID, m.TraceID, m.SessionID, m.UserID, m.Status, m.Degraded, m.DegradedReason,
		m.LLMCalls, m.MaxIterations, m.MaxIterationsHit,
		m.ToolCalls, m.ToolErrors, m.DistinctTools, m.Compactions,
		m.PromptTokens, m.CachedTokens, m.CompletionTokens, m.TotalTokens, m.CostCNY,
		m.DurationMS, m.ProjectName, m.StartedAt, endedAt,
	)
	if err != nil {
		n := w.failed.Add(1)
		if n == 1 || n%64 == 0 {
			logger.Warn("telemetry: write run_metrics failed run_id=%s failed_total=%d err=%v", m.RunID, n, err)
		}
		return
	}
	w.written.Add(1)
}
