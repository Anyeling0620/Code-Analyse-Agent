package cost

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/cost"
	"edu.agent.code/config"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"
	"edu.agent.code/utils/logger"
	"edu.agent.code/utils/pricing"
	"fmt"
	"time"
)

type Service struct {
	cost  cost.ICost
	price config.ModelPrice
}

func NewService(adaptor adaptor.IAdaptor) *Service {
	return &Service{
		cost:  cost.NewCost(adaptor),
		price: adaptor.GetConfig().ModelPrice,
	}
}

// Track TODO: 价格计算按高峰期计算，没有分时期
func (s *Service) Track(ctx context.Context, userID, sessionID, modelName, toolName string, prompt, completion int64) error {
	if prompt < 0 && completion < 0 {
		return nil
	}
	rate, ok := s.lookupRate(modelName)
	if !ok {
		err := fmt.Errorf("model %s not found", modelName)
		logger.Warn("cost:%v, fallback=%s", err, s.price.FallbackModel)
		return err
	}
	cny := pricing.CalculateCNY(rate, prompt, completion)
	err := s.cost.Insert(ctx, &do.CostRecord{
		UserID:           userID,
		SessionID:        sessionID,
		Model:            modelName,
		ToolName:         toolName,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		EstimatedCNY:     cny,
		OccurredAt:       time.Now(),
	})
	if err != nil {
		logger.Warn("cost:%v, insert cost record failed, user=%s, session=%s, model=%s", err, userID, sessionID, modelName)
		return err
	}
	return nil
}

func (s *Service) lookupRate(modelName string) (pricing.Rate, bool) {
	if p, ok := s.price.PriceCNY[modelName]; ok {
		return pricing.Rate{
			Prompt:     p.Prompt,
			Completion: p.Completion,
		}, true
	}
	if s.price.FallbackModel != "" {
		if p, ok := s.price.PriceCNY[s.price.FallbackModel]; ok {
			return pricing.Rate{
				Prompt:     p.Prompt,
				Completion: p.Completion,
			}, true
		}
	}
	return pricing.Rate{}, false
}

func (s *Service) DailyTotal(ctx context.Context, day time.Time) (float64, error) {
	total, err := s.cost.DailyTotal(ctx, day)
	if err != nil {
		logger.Error("cost.DailyTotal days=%s err=%v", day.Format(time.DateTime), err)
		return 0, err
	}
	return total, nil
}

func (s *Service) GroupByUser(ctx context.Context, from, to time.Time) ([]dto.CostByUser, error) {
	rows, err := s.cost.GroupByUser(ctx, from, to)
	if err != nil {
		logger.Error("cost: GroupByUser GroupByUser err: %v, from=%s to=%s", err, from.Format(time.DateTime), to.Format(time.DateTime))
		return nil, err
	}
	results := make([]dto.CostByUser, 0)
	for _, row := range rows {
		results = append(results, dto.CostByUser{
			UserID: row.UserID,
			Tokens: row.Tokens,
			CNY:    row.CNY,
		})
	}
	return results, nil
}

func (s *Service) GroupByTool(ctx context.Context, from, to time.Time) ([]dto.CostByTool, error) {
	rows, err := s.cost.GroupByTool(ctx, from, to)
	if err != nil {
		logger.Error("cost: GroupByTool GroupByTool err: %v, from=%s to=%s", err, from.Format(time.DateTime), to.Format(time.DateTime))
		return nil, err
	}
	results := make([]dto.CostByTool, 0)
	for _, row := range rows {
		results = append(results, dto.CostByTool{
			ToolName: row.ToolName,
			Tokens:   row.Tokens,
			CNY:      row.CNY,
		})
	}
	return results, nil
}
