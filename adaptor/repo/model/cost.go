package model

import "time"

type CostRecord struct {
	ID               string `gorm:"primary_key;AUTO_INCREMENT"`
	UserID           string `gorm:"size:128;index"`
	SessionID        string `gorm:"size:128;index"`
	Model            string `gorm:"size:32"`
	ToolName         string `gorm:"size:64;index"`
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	EstimatedCNY     float64   `gorm:"type:decimal(10,2)"`
	OccurredAt       time.Time `gorm:"index"`
}

func (CostRecord) TableName() string {
	return "cost_records"
}
