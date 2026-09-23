package do

import "time"

type CostRecord struct {
	UserID           string
	SessionID        string
	Model            string
	ToolName         string
	PromptTokens     int64
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
