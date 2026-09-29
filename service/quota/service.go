package quota

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/quota"
	"edu.agent.code/common"
	"edu.agent.code/utils/limiter"
	"edu.agent.code/utils/logger"
)

type Service struct {
	quota   quota.IQuota
	limiter *limiter.Limiter
	// guestDailyLimit 覆盖游客身份的单日调用上限，<=0 表示沿用套餐额度。
	guestDailyLimit int
}

func NewService(adaptor adaptor.IAdaptor) *Service {
	return &Service{
		quota:           quota.NewQuota(adaptor),
		limiter:         limiter.NewLimiter(),
		guestDailyLimit: adaptor.GetConfig().Auth.Guest.DailyQuota,
	}
}

// DailyLimit 返回该身份的单日调用上限：游客可用 auth.guest.daily_quota 单独收紧，
// 其余身份按套餐额度。配额展示与配额拦截都走这里，避免两处口径不一致。
func (s *Service) DailyLimit(user *common.UserInfo) int {
	if user == nil {
		return 0
	}
	if s.guestDailyLimit > 0 && common.IsGuestUser(user.UserID) {
		return s.guestDailyLimit
	}
	return user.Plan.DailyQuota()
}

func (s *Service) Today(ctx context.Context, userID, day string) (int64, error) {
	todayCount, err := s.quota.QueryByToday(ctx, userID, day)
	if err != nil {
		logger.Error("quota: QueryByDay Error, user=%s, day=%s, err=%v", userID, day, err)
		return 0, err
	}
	return todayCount, nil
}

func (s *Service) Increment(ctx context.Context, userID, day string) (int64, error) {
	count, err := s.quota.Increment(ctx, userID, day)
	if err != nil {
		logger.Error("quota: Increment Error, user=%s, day=%s, err=%v", userID, day, err)
		return 0, err
	}
	return count, nil
}

// 限流应使用redis限流，但是目前demo为了实现快速，使用了简单的令牌桶，但是这种方法在多节点时会导致double问题

func (s *Service) IsAllow(userID string, burstPerMinute int) bool {
	return s.limiter.Allow(userID, burstPerMinute)
}
