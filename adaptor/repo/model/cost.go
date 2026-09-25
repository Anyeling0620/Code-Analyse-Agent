package model

import "time"

type CostRecord struct {
	ID               int64  `gorm:"primaryKey;autoIncrement"` // Check 主键需为整型自增才能匹配物理列 cost_records.id(integer PRIMARY KEY AUTOINCREMENT)；原 string + 未被解析的 AUTO_INCREMENT 会让 GORM 以空串写入 id，触发 datatype mismatch
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
