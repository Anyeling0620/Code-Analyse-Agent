package do

import "time"

// User 是邮箱注册用户的领域对象。
// Email 为规范化后的小写邮箱，同时用作 common.UserInfo.UserID。
type User struct {
	ID           uint
	Email        string
	PasswordHash string
	Plan         string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastLoginAt  *time.Time
}
