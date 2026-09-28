package do

import "time"

type CostRecord struct {
	UserID           string
	SessionID        string
	Model            string
	ToolName         string
	PromptTokens     int64
	CachedTokens     int64 // 命中 prompt cache 的输入 token 数
	CacheMissTokens  int64 // 未命中 cache 的输入 token 数
	CompletionTokens int64
	EstimatedCNY     float64 // 总成本，等于下面三段之和
	CacheHitCNY      float64 // 命中部分成本
	CacheMissCNY     float64 // 未命中部分成本
	OccurredAt       time.Time
}

// CostDailyUsage 是某一天的用量与成本汇总（含缓存命中/未命中拆分）。
type CostDailyUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	CacheMissTokens  int64
	TotalCNY         float64
	CacheHitCNY      float64
	CacheMissCNY     float64
}

type CostByUser struct {
	UserID string
	Tokens int64
	CNY    float64
}

type CostByTool struct {
	ToolName string
	Tokens   int64
	CNY      float64
}
