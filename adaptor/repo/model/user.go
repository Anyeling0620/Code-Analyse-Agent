package model

import "time"

// User 是邮箱注册用户的账号表。
// Email 在入库前由业务层统一规范化为小写去空格，唯一索引兜底并发重复注册。
type User struct {
	ID           uint   `gorm:"primaryKey;autoIncrement"`
	Email        string `gorm:"size:254;uniqueIndex;not null"`
	PasswordHash string `gorm:"size:100;not null"`
	Plan         string `gorm:"size:16;not null;default:free"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastLoginAt  *time.Time
}

// TableName 返回注册用户表名。
func (User) TableName() string { return "users" }
