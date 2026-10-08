package conversation

import (
	"context"
	"edu.agent.code/service/dto"
	"edu.agent.code/service/telemetry"
	"time"
)

// shipRunMetrics 把本轮 run 的聚合指标投递给埋点库（异步、非阻塞）。
//
// 只在终态调用。注意一次 run 可能被调用两次：审批中断写一次 interrupted，
// resume 跑完再写一次 done——埋点表用 upsert 把同一个 run_id 收敛成一行，
// 后写的终态覆盖先写的。
//
// 刻意不返回错误也不阻塞：埋点失败不该影响 run 状态机、落库和前端事件。
func (s *Service) shipRunMetrics(ctx context.Context, runState *dto.ChatRunState, session *dto.SessionContext, status string) {
	if s == nil || s.telemetry == nil || runState == nil || runState.RunID == "" {
		return
	}
	projectName := ""
	if session != nil {
		projectName = session.CurrentProjectName
	}
	// Duration 是墙钟时间：被审批中断过的 run，这段里包含审批等待时间。
	endedAt := time.Now()
	startedAt := runState.StartedAt
	if startedAt.IsZero() {
		// run_metrics.started_at 是 NOT NULL，零值时间在 MySQL 严格模式下写不进去，
		// 兜底用结束时刻，宁可耗时为 0 也不要丢这一行指标。
		startedAt = endedAt
	}
	s.telemetry.Write(telemetry.RunMetrics{
		RunID:            runState.RunID,
		TraceID:          runState.TraceID,
		SessionID:        runState.SessionID,
		UserID:           runState.UserID,
		Status:           status,
		Degraded:         runState.Degraded,
		DegradedReason:   runState.DegradedReason,
		LLMCalls:         runState.LLMCalls,
		MaxIterations:    s.maxIterations(),
		MaxIterationsHit: runState.DegradedReason == dto.DegradedReasonMaxIterations,
		ToolCalls:        runState.ToolCalls,
		ToolErrors:       runState.ToolErrors,
		DistinctTools:    len(runState.UsedTools),
		// 压缩次数由压缩中间件通过 ctx 上的计数器回传（见 dto.RunCounters）。
		Compactions:      dto.RunCountersFromContext(ctx).Compactions(),
		PromptTokens:     runState.PromptTokens,
		CachedTokens:     runState.CachedTokens,
		CompletionTokens: runState.CompletionTokens,
		TotalTokens:      runState.PromptTokens + runState.CompletionTokens,
		CostCNY:          runState.CostCNY,
		DurationMS:       endedAt.Sub(startedAt).Milliseconds(),
		ProjectName:      projectName,
		StartedAt:        startedAt,
		EndedAt:          endedAt,
	})
}

// maxIterations 返回主 agent 配置的迭代上限。
// 报表要回答的是"阈值定得合不合理"，所以必须把阈值和实际轮次一起存下来，
// 只存实际轮次的话，改过配置的历史数据就无法回溯解释了。
func (s *Service) maxIterations() int {
	if s == nil || s.conf == nil {
		return 0
	}
	return s.conf.Agents.MaxIterations.Compose
}
