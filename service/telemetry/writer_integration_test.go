package telemetry

import (
	"context"
	"database/sql"
	"edu.agent.code/config"
	"os"
	"testing"
	"time"
)

// TestWriterUpsertAgainstMySQL 是对真实 MySQL 的集成测试：验证 upsertRunMetrics
// 的 SQL 与参数绑定在真实列类型上成立（bool → TINYINT(1)、time.Time → DATETIME(3)、
// DECIMAL(12,6)），以及"同一 run_id 写两次收敛成一行、后写覆盖先写"。
//
// 这些是单测覆盖不到的部分：SQL 拼错了、驱动把 bool 绑成 '1'/'0' 字符串之类的问题，
// 只有真连数据库才会暴露。
//
// 默认跳过；要跑就把写账号的 DSN 放进环境变量：
//
//	AGENT_TELEMETRY_TEST_DSN='agent_telemetry_w:pw@tcp(host:3306)/agent_telemetry?...' \
//	  go test ./service/telemetry -run TestWriterUpsertAgainstMySQL -v
//
// 注意：写账号只有 SELECT/INSERT/UPDATE，测试**不会**自己清理数据行，
// 需要跑完后用管理账号删掉 run_id 以 __go_probe__ 开头的那行。
func TestWriterUpsertAgainstMySQL(t *testing.T) {
	dsn := os.Getenv("AGENT_TELEMETRY_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 AGENT_TELEMETRY_TEST_DSN，跳过 MySQL 集成测试")
	}

	writer, err := NewWriter(config.Telemetry{Enable: true, DSN: dsn, QueueSize: 8, WriteTimeoutSec: 5})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	started := time.Now().Truncate(time.Millisecond)

	// 第一次：模拟审批中断。
	writer.Write(RunMetrics{
		RunID: "__go_probe__", TraceID: "t1", SessionID: "s1", UserID: "u1",
		Status: "interrupted", Degraded: false, DegradedReason: "",
		LLMCalls: 3, MaxIterations: 40, ToolCalls: 2, DistinctTools: 1,
		StartedAt: started,
	})
	// 第二次：模拟 resume 后跑完 done —— 必须覆盖上一行。
	writer.Write(RunMetrics{
		RunID: "__go_probe__", TraceID: "t1", SessionID: "s1", UserID: "u1",
		Status: "done", Degraded: true, DegradedReason: "max_iterations",
		LLMCalls: 7, MaxIterations: 40, MaxIterationsHit: true,
		ToolCalls: 5, ToolErrors: 1, DistinctTools: 3, Compactions: 2,
		PromptTokens: 1234, CachedTokens: 1000, CompletionTokens: 56, TotalTokens: 1290,
		CostCNY: 0.123456, DurationMS: 4567, ProjectName: "probe-project",
		StartedAt: started, EndedAt: started.Add(4567 * time.Millisecond),
	})

	// Close 会先排空队列再退出，之后的查询一定能看到数据。
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open for verify: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var (
		status         string
		degraded       bool
		degradedReason string
		llmCalls       int
		maxHit         bool
		toolErrors     int
		compactions    int
		totalTokens    int64
		cost           float64
		durationMS     int64
		projectName    string
	)
	row := db.QueryRowContext(ctx,
		`SELECT status, degraded, degraded_reason, llm_calls, max_iterations_hit,
		        tool_errors, compactions, total_tokens, cost_cny, duration_ms, project_name
		   FROM run_metrics WHERE run_id = '__go_probe__'`)
	if err := row.Scan(&status, &degraded, &degradedReason, &llmCalls, &maxHit,
		&toolErrors, &compactions, &totalTokens, &cost, &durationMS, &projectName); err != nil {
		t.Fatalf("查询回读失败（upsert 可能没写进去）: %v", err)
	}

	// 只应有一行：两次写入按 run_id 收敛。
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_metrics WHERE run_id = '__go_probe__'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("upsert 未收敛：run_metrics 里有 %d 行，期望 1 行", count)
	}

	if status != "done" || !degraded || degradedReason != "max_iterations" {
		t.Fatalf("后写未覆盖先写：status=%q degraded=%v reason=%q", status, degraded, degradedReason)
	}
	if !maxHit || llmCalls != 7 || toolErrors != 1 || compactions != 2 {
		t.Fatalf("计数列不符：maxHit=%v llm=%d toolErrors=%d compactions=%d", maxHit, llmCalls, toolErrors, compactions)
	}
	if totalTokens != 1290 || durationMS != 4567 || projectName != "probe-project" {
		t.Fatalf("数值列不符：tokens=%d duration=%d project=%q", totalTokens, durationMS, projectName)
	}
	if cost < 0.123455 || cost > 0.123457 {
		t.Fatalf("DECIMAL 精度不符：cost=%v", cost)
	}
	t.Logf("MySQL 集成验证通过：单行收敛、后写覆盖、类型绑定正确（cost=%v）", cost)
}
