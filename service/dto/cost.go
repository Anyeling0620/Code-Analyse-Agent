package dto

type CostDailyTotal struct {
	Date             string  `json:"date"`
	CNY              float64 `json:"cny"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CacheHitTokens   int64   `json:"cache_hit_tokens"`
	CacheMissTokens  int64   `json:"cache_miss_tokens"`
	CacheHitCNY      float64 `json:"cache_hit_cny"`
	CacheMissCNY     float64 `json:"cache_miss_cny"`
}

type CostByUser struct {
	UserID string  `json:"user_id"`
	Tokens int64   `json:"tokens"`
	CNY    float64 `json:"cny"`
}

type CostByTool struct {
	ToolName string  `json:"tool_name"`
	Tokens   int64   `json:"tokens"`
	CNY      float64 `json:"cny"`
}
