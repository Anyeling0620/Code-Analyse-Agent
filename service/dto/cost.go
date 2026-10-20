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

// CostOverview 是「个人消耗总览」的一次性返回体：总量 + 三个维度 + 每日曲线，
// 前端一次拉齐即可渲染整页，不再按维度分别请求。
type CostOverview struct {
	From      string             `json:"from"`
	To        string             `json:"to"`
	Total     CostOverviewTotal  `json:"total"`
	ByTool    []CostOverviewItem `json:"by_tool"`
	ByModel   []CostOverviewItem `json:"by_model"`
	BySession []CostOverviewItem `json:"by_session"`
	Daily     []CostDailyPoint   `json:"daily"`
}

type CostOverviewTotal struct {
	CNY              float64 `json:"cny"`
	Calls            int64   `json:"calls"`
	Sessions         int64   `json:"sessions"`
	Tools            int64   `json:"tools"`
	Models           int64   `json:"models"`
	Tokens           int64   `json:"tokens"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CacheHitTokens   int64   `json:"cache_hit_tokens"`
	CacheMissTokens  int64   `json:"cache_miss_tokens"`
	CacheHitCNY      float64 `json:"cache_hit_cny"`
	CacheMissCNY     float64 `json:"cache_miss_cny"`
}

// CostOverviewItem 是分工具 / 分模型 / 分会话的公共行结构。
// Share 保留 4 位小数（纯展示比例，足够前端画占比条），金额本身不取整由前端格式化。
type CostOverviewItem struct {
	Key    string  `json:"key"`
	Label  string  `json:"label"`
	Calls  int64   `json:"calls"`
	Tokens int64   `json:"tokens"`
	CNY    float64 `json:"cny"`
	Share  float64 `json:"share"`
	LastAt string  `json:"last_at"`
}

// CostDailyPoint 是每日曲线上的一个点，区间内没有记录的日子也会补 0 点。
type CostDailyPoint struct {
	Date   string  `json:"date"`
	Calls  int64   `json:"calls"`
	Tokens int64   `json:"tokens"`
	CNY    float64 `json:"cny"`
}
