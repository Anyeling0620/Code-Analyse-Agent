package model

// QuotaUsage 用户日配额计数。(user_id, day) 组合主键。
type QuotaUsage struct {
	UserID string `gorm:"primaryKey;size:128"` // 配额归属用户。
	Day    string `gorm:"primaryKey;size:10"`  // YYYY-MM-DD，本地日期维度。
	Count  int64  // 当日已消耗请求次数。
}

// TableName 返回配额计数表名。
func (QuotaUsage) TableName() string { return "quota_usage" }
