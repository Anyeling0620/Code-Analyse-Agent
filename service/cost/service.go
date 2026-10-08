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

// Track 记录一次模型调用的用量与成本，返回这次调用的成本（元）。
//
// 返回值给调用方累加成 run 级成本；命中不到单价时返回 0 与 error，
// 调用方应把该次调用按 0 成本累计而不是丢弃。
// prompt 为总输入 token，cached 为其中命中 prompt cache 的部分（未命中部分 = prompt - cached），
// cached 超出 [0, prompt] 时会被收敛到合法区间。
// TODO: 价格计算按高峰期计算，没有分时期
func (s *Service) Track(ctx context.Context, userID, sessionID, modelName, toolName string, prompt, cached, completion int64) (float64, error) {
	if prompt < 0 || cached < 0 || completion < 0 { // Check 原为 && 导致只屏蔽"双负数"，任一为负都应视为无效用量
		return 0, nil
	}
	if cached > prompt {
		cached = prompt
	}
	rate, err := s.lookupRate(modelName)
	if err != nil {
		logger.Warn("cost:%v, fallback=%s", err, s.price.FallbackModel)
		return 0, err
	}

	breakdown := pricing.Calculate(rate, prompt, cached, completion)
	cny := breakdown.TotalCNY()
	err = s.cost.Insert(ctx, &do.CostRecord{
		UserID:           userID,
		SessionID:        sessionID,
		Model:            modelName,
		ToolName:         toolName,
		PromptTokens:     prompt,
		CachedTokens:     cached,
		CacheMissTokens:  prompt - cached,
		CompletionTokens: completion,
		EstimatedCNY:     cny,
		CacheHitCNY:      breakdown.CacheHitCNY,
		CacheMissCNY:     breakdown.CacheMissCNY,
		OccurredAt:       time.Now(),
	})
	if err != nil {
		logger.Warn("cost:%v, insert cost record failed, user=%s, session=%s, model=%s", err, userID, sessionID, modelName)
		return cny, err
	}
	return cny, nil
}

// lookupRate 查询模型单价：模型未配置，或缓存命中价未正确配置时返回错误。
func (s *Service) lookupRate(modelName string) (pricing.Rate, error) {
	if p, ok := s.price.PriceCNY[modelName]; ok {
		return toRate(modelName, p)
	}
	if s.price.FallbackModel != "" {
		if p, ok := s.price.PriceCNY[s.price.FallbackModel]; ok {
			return toRate(s.price.FallbackModel, p)
		}
	}
	return pricing.Rate{}, fmt.Errorf("model %s not found", modelName)
}

// toRate 把配置价转换为计价单价。
// cache_hit 必须显式配置为正数：缺失或非正会让成本被低估，因此直接报错而不做兜底。
func toRate(modelName string, p config.ModelPriceCNY) (pricing.Rate, error) {
	if p.CacheHit <= 0 {
		return pricing.Rate{}, fmt.Errorf("model %s cache_hit price must be > 0, got %v", modelName, p.CacheHit)
	}
	return pricing.Rate{
		Prompt:     p.Prompt,
		CacheHit:   p.CacheHit,
		Completion: p.Completion,
	}, nil
}

func (s *Service) DailyTotal(ctx context.Context, day time.Time) (float64, error) {
	total, err := s.cost.DailyTotal(ctx, day)
	if err != nil {
		logger.Error("cost.DailyTotal days=%s err=%v", day.Format(time.DateTime), err)
		return 0, err
	}
	return total, nil
}

// DailyUsage 返回某天、某个用户的用量与成本汇总，含缓存命中 / 未命中拆分，供前端展示。
func (s *Service) DailyUsage(ctx context.Context, userID string, day time.Time) (dto.CostDailyTotal, error) {
	row, err := s.cost.DailyUsage(ctx, userID, day)
	if err != nil {
		logger.Error("cost.DailyUsage user=%s days=%s err=%v", userID, day.Format(time.DateTime), err)
		return dto.CostDailyTotal{}, err
	}
	return dto.CostDailyTotal{
		Date:             day.Format(time.DateOnly),
		CNY:              row.TotalCNY,
		PromptTokens:     row.PromptTokens,
		CompletionTokens: row.CompletionTokens,
		CacheHitTokens:   row.CachedTokens,
		CacheMissTokens:  row.CacheMissTokens,
		CacheHitCNY:      row.CacheHitCNY,
		CacheMissCNY:     row.CacheMissCNY,
	}, nil
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
