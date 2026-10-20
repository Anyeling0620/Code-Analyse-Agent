package user

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/model"
	"edu.agent.code/service/do"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// IUser 是邮箱注册用户的仓储接口。
type IUser interface {
	// GetByEmail 按规范化邮箱查询用户；不存在时返回 (nil, nil)。
	GetByEmail(ctx context.Context, email string) (*do.User, error)
	// Create 创建用户。邮箱唯一索引冲突时返回 IsUniqueViolation(err)==true 的错误。
	Create(ctx context.Context, user *do.User) error
	// UpdateLastLogin 更新最近登录时间；用户不存在时静默返回 nil。
	UpdateLastLogin(ctx context.Context, email string, at time.Time) error
	// UpdatePassword 更新密码哈希；用户不存在时静默返回 nil。
	UpdatePassword(ctx context.Context, email, passwordHash string) error
}

// User 是 IUser 的 GORM 实现。
type User struct {
	db *gorm.DB
}

// NewUser 从 adaptor 取 DB 构造仓储。
func NewUser(adaptor adaptor.IAdaptor) *User {
	return NewUserWithDB(adaptor.GetDB())
}

// NewUserWithDB 直接用 *gorm.DB 构造仓储，便于单测注入内存/临时 SQLite。
func NewUserWithDB(db *gorm.DB) *User {
	return &User{db: db}
}

func (u *User) GetByEmail(ctx context.Context, email string) (*do.User, error) {
	var row model.User
	err := u.db.WithContext(ctx).
		Where("email = ?", normalizeEmail(email)).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return toDomain(&row), nil
}

func (u *User) Create(ctx context.Context, user *do.User) error {
	if user == nil {
		return nil
	}
	row := &model.User{
		Email:        normalizeEmail(user.Email),
		PasswordHash: user.PasswordHash,
		Plan:         user.Plan,
		LastLoginAt:  user.LastLoginAt,
	}
	if err := u.db.WithContext(ctx).Create(row).Error; err != nil {
		return err
	}
	user.ID = row.ID
	user.Email = row.Email
	user.CreatedAt = row.CreatedAt
	user.UpdatedAt = row.UpdatedAt
	return nil
}

func (u *User) UpdateLastLogin(ctx context.Context, email string, at time.Time) error {
	return u.db.WithContext(ctx).
		Model(&model.User{}).
		Where("email = ?", normalizeEmail(email)).
		Update("last_login_at", at).Error
}

func (u *User) UpdatePassword(ctx context.Context, email, passwordHash string) error {
	return u.db.WithContext(ctx).
		Model(&model.User{}).
		Where("email = ?", normalizeEmail(email)).
		Update("password_hash", passwordHash).Error
}

// IsUniqueViolation 判断是否为邮箱唯一索引冲突。
//
// GORM 的 TranslateError 默认没有开启，这里同时兼容 SQLite 的原始错误文案
// 和 GORM 的 ErrDuplicatedKey，避免不同驱动下并发注册兜底失效。
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") ||
		strings.Contains(msg, "duplicate entry") ||
		strings.Contains(msg, "unique violation")
}

// normalizeEmail 与业务层使用同一套规范化规则：去空格并转小写。
// 仓储层再兜一层，保证调用方即使忘了规范化也不会写出大小写不同的重复账号。
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func toDomain(row *model.User) *do.User {
	if row == nil {
		return nil
	}
	return &do.User{
		ID:           row.ID,
		Email:        row.Email,
		PasswordHash: row.PasswordHash,
		Plan:         row.Plan,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
		LastLoginAt:  row.LastLoginAt,
	}
}
