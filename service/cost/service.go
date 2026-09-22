package cost

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/cost"
	"edu.agent.code/utils/logger"
	"time"
)

type Service struct {
	cost cost.ICost
}

func NewService(adaptor adaptor.IAdaptor) *Service {
	return &Service{
		cost: cost.NewCost(adaptor),
	}
}

func (s *Service) DailyTotal(ctx context.Context, day time.Time) (float64, error) {
	total, err := s.cost.DailyTotal(ctx, day)
	if err != nil {
		logger.Error("cost.DailyTotal days=%s err=%v", day.Format(time.DateTime), err)
		return 0, err
	}
	return total, nil
}
