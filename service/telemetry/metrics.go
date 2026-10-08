package telemetry

import "time"

// RunMetrics 是一轮 run 结束时投递的聚合指标。
//
// 只承载数字与 id：**不包含问题、回答或工具入参**等对话原文。
// 这样即便报表侧口径写错，也不会把用户对话内容泄漏出去。
type RunMetrics struct {
	RunID     string
	TraceID   string
	SessionID string
	UserID    string

	// Status 是终态：done / failed / interrupted。
	Status string
	// Degraded 标记本轮是降级完成（部分报告）。
	Degraded bool
	// DegradedReason 区分降级原因：max_iterations（迭代耗尽）/ error_partial（报错但留了半成品）。
	DegradedReason string

	// LLMCalls 是模型调用次数，等价于迭代轮次：流式场景下只有末帧带 Usage，
	// 因此"带 Usage 的消息数"就是模型调用数。
	LLMCalls int
	// MaxIterations 是主 agent 配置的迭代上限，用于和 LLMCalls 对比判断阈值是否合理。
	MaxIterations int
	// MaxIterationsHit 标记本轮是否命中了迭代上限。
	MaxIterationsHit bool

	ToolCalls     int
	ToolErrors    int
	DistinctTools int
	// Compactions 是上下文压缩（compaction）在本轮触发的次数。
	Compactions int

	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CostCNY          float64

	// DurationMS 是 started_at → ended_at 的墙钟耗时。
	// 注意：被审批中断后又 resume 的 run，这段时间里包含审批等待时间。
	DurationMS int64

	ProjectName string
	StartedAt   time.Time
	EndedAt     time.Time
}
