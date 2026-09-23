package dto

type CostDailyTotal struct {
	Date string  `json:"date"`
	CNY  float64 `json:"cny"`
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
