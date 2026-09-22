package cost

import (
	"context"
	"edu.agent.code/adaptor"
	"time"
)

type Service struct {
}

func NewService(adaptor adaptor.IAdaptor) *Service {
	return &Service{}
}

func (s *Service) DailyTotal(ctx context.Context, day time.Time) (float64, error) {
	return 100, nil
}
