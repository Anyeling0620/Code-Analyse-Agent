package common

import "strings"

const (
	// CtxKeyUserID 是 gin.Context 中当前用户 ID 的 key。
	CtxKeyUserID = "user_id"
	// CtxKeyTraceID 是 gin.Context 中 trace id 的 key。
	CtxKeyTraceID = "trace_id"
	// CtxKeyAuthUser 是 gin.Context 中 *UserInfo 的 key。
	CtxKeyAuthUser = "auth_user"
	// CtxKeyAuthToken 是 gin.Context 中当前请求所用登录令牌的 key，供登出时按令牌吊销。
	CtxKeyAuthToken = "auth_token"
	// GuestUserPrefix 是旧版游客身份的 UserID 前缀。
	// 游客登录已下线；保留该前缀只为在令牌校验时拒绝历史游客令牌。
	GuestUserPrefix = "guest_"
)

// IsGuestUser 判断 UserID 是否为旧版游客派生身份。
func IsGuestUser(userID string) bool {
	return strings.HasPrefix(userID, GuestUserPrefix)
}

type Plan string

const (
	PlanFree Plan = "free"
	PlanPlus Plan = "plus"
	PlanPro  Plan = "pro"
)

type UserInfo struct {
	UserID string `json:"user_id"`
	Plan   Plan   `json:"plan"`
	// IssuedAt 是令牌签发时间（UnixNano）。密码重设后用它拒绝旧令牌；
	// 历史令牌没有该字段，值为 0，密码一旦重设就会被判为过期。
	IssuedAt int64 `json:"issued_at,omitempty"`
}

func Ptr[T any](v T) *T {
	return &v
}

func (p Plan) DailyQuota() int {
	switch p {
	case PlanFree:
		return 10
	case PlanPlus:
		return 100
	case PlanPro:
		return 200
	default:
		return 10
	}
}

func (p Plan) BurstPerMinute() int {
	switch p {
	case PlanPlus:
		return 100
	case PlanPro:
		return 200
	default:
		return 10
	}
}
