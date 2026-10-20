package quota

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/quota"
	"edu.agent.code/common"
	"edu.agent.code/config"
	"edu.agent.code/utils/limiter"
	"edu.agent.code/utils/logger"
)

type Service struct {
	quota   quota.IQuota
	limiter *limiter.Limiter
}

func NewService(adaptor adaptor.IAdaptor) *Service {
	conf := adaptor.GetConfig()
	var limitConf config.RateLimit
	if conf != nil {
		limitConf = conf.RateLimit
	}
	return &Service{
		quota: quota.NewQuota(adaptor),
		// 令牌桶放在 Redis 里：多实例共享同一份额度，进程重启也不会把额度重置。
		limiter: limiter.New(adaptor.GetRedis(), limitConf),
	}
}

// Limiter 暴露限流器给路由层做 IP 级限流，整个进程共用一份配置。
func (s *Service) Limiter() *limiter.Limiter {
	if s == nil {
		return nil
	}
	return s.limiter
}

// DailyLimit 返回该身份的单日调用上限：按套餐额度。
// 配额展示与配额拦截都走这里，避免两处口径不一致。
func (s *Service) DailyLimit(user *common.UserInfo) int {
	if user == nil {
		return 0
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

// IsAllow 按用户维度消耗一个令牌。capacity 取套餐的每分钟突发额度，
// 桶本身在 Redis 中，多实例下放行量仍然是「一人一份」。
func (s *Service) IsAllow(ctx context.Context, userID string, burstPerMinute int) limiter.Decision {
	// key 前缀与入口的 user: 区分开：入口限流统计「所有接口」，这里统计「聊天额度」，两个桶。
	return s.limiter.Allow(ctx, "chat:user:"+userID, burstPerMinute)
}
