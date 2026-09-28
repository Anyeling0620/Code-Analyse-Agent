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
	EstimatedCNY     float64
	OccurredAt       time.Time
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
