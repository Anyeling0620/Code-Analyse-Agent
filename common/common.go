package common

const (
	// CtxKeyUserID 是 gin.Context 中当前用户 ID 的 key。
	CtxKeyUserID = "user_id"
	// CtxKeyTraceID 是 gin.Context 中 trace id 的 key。
	CtxKeyTraceID = "trace_id"
	// CtxKeyAuthUser 是 gin.Context 中 *UserInfo 的 key。
	CtxKeyAuthUser = "auth_user"
)

type Plan string

const (
	PlanFree Plan = "free"
	PlanPlus Plan = "plus"
	PlanPro  Plan = "pro"
)

type UserInfo struct {
	UserID string `json:"user_id"`
	Plan   Plan   `json:"plan"`
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
