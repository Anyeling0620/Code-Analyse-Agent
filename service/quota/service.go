package quota

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/quota"
	"edu.agent.code/utils/limiter"
	"edu.agent.code/utils/logger"
)

type Service struct {
	quota   quota.IQuota
	limiter *limiter.Limiter
}

func NewService(adaptor adaptor.IAdaptor) *Service {
	return &Service{
		quota:   quota.NewQuota(adaptor),
		limiter: limiter.NewLimiter(),
	}
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
