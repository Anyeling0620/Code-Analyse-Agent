package cost

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/model"
	"edu.agent.code/service/do"
	"gorm.io/gorm"
	"time"
)

type ICost interface {
	Insert(ctx context.Context, req *do.CostRecord) error
	DailyTotal(ctx context.Context, date time.Time) (float64, error)
	DailyUsage(ctx context.Context, userID string, date time.Time) (do.CostDailyUsage, error)
	GroupByUser(ctx context.Context, from, to time.Time) ([]do.CostByUser, error)
	GroupByTool(ctx context.Context, from, to time.Time) ([]do.CostByTool, error)
}

type Cost struct {
	db *gorm.DB
}

func NewCost(adaptor adaptor.IAdaptor) *Cost {
	return &Cost{
		db: adaptor.GetDB(),
	}
}

func (c *Cost) Insert(ctx context.Context, req *do.CostRecord) error {
	return c.db.WithContext(ctx).Create(&model.CostRecord{
		UserID:           req.UserID,
		SessionID:        req.SessionID,
		Model:            req.Model,
		ToolName:         req.ToolName,
		PromptTokens:     req.PromptTokens,
		CachedTokens:     req.CachedTokens,
		CacheMissTokens:  req.CacheMissTokens,
		CompletionTokens: req.CompletionTokens,
		TotalTokens:      req.PromptTokens + req.CompletionTokens,
		EstimatedCNY:     req.EstimatedCNY,
		CacheHitCNY:      req.CacheHitCNY,
		CacheMissCNY:     req.CacheMissCNY,
		OccurredAt:       req.OccurredAt,
	}).Error
}

func (c *Cost) DailyTotal(ctx context.Context, date time.Time) (float64, error) {
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	end := start.Add(24 * time.Hour)

	var total float64
	err := c.db.WithContext(ctx).Model(&model.CostRecord{}).
		Where("occurred_at >= ? AND occurred_at < ?", start, end).
		Select("COALESCE(SUM(estimated_cny), 0)"). // Check 原缺少 SUM 聚合，多行时只取到最后一条记录金额；COALESCE 保证区间无记录时返回 0 而不是 NULL
		Scan(&total).Error
	return total, err
}

// DailyUsage 汇总某天、某个用户的用量与成本，含缓存命中 / 未命中的拆分。
// 必须按 user_id 过滤：成本记录是按用户记账的，不带条件就是全站汇总，
// 会让每个账号都看到同一份"全站成本"。
// 未命中 token 用 SUM(prompt_tokens) - SUM(cached_tokens) 而不是 SUM(cache_miss_tokens)：
// 加列之前写入的历史记录 cache_miss_tokens 为 0，取差值才能与 prompt_tokens 保持一致。
func (c *Cost) DailyUsage(ctx context.Context, userID string, date time.Time) (do.CostDailyUsage, error) {
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	end := start.Add(24 * time.Hour)

	var row do.CostDailyUsage
	err := c.db.WithContext(ctx).Model(&model.CostRecord{}).
		Where("user_id = ? AND occurred_at >= ? AND occurred_at < ?", userID, start, end).
		Select("COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens, " +
			"COALESCE(SUM(completion_tokens), 0) AS completion_tokens, " +
			"COALESCE(SUM(cached_tokens), 0) AS cached_tokens, " +
			"COALESCE(SUM(prompt_tokens), 0) - COALESCE(SUM(cached_tokens), 0) AS cache_miss_tokens, " +
			"COALESCE(SUM(estimated_cny), 0) AS total_cny, " +
			"COALESCE(SUM(cache_hit_cny), 0) AS cache_hit_cny, " +
			"COALESCE(SUM(cache_miss_cny), 0) AS cache_miss_cny").
		Scan(&row).Error
	return row, err
}

// GroupByUser 按用户聚合区间内的总 token 与成本。 。
func (c *Cost) GroupByUser(ctx context.Context, from, to time.Time) ([]do.CostByUser, error) {
	rows := []do.CostByUser{}
	err := c.db.WithContext(ctx).Model(&model.CostRecord{}).
		Where("occurred_at >= ? AND occurred_at < ?", from, to).
		Select("user_id, SUM(total_tokens) AS tokens, SUM(estimated_cny) AS cny").
		Group("user_id").
		Order("cny DESC").
		Scan(&rows).Error
	return rows, err
}

// GroupByTool 按工具聚合区间内的总 token 与成本，空 tool_name 归入 (direct)。
func (c *Cost) GroupByTool(ctx context.Context, from, to time.Time) ([]do.CostByTool, error) {
	const toolExpr = "COALESCE(NULLIF(tool_name, ''), '(direct)')"
	rows := []do.CostByTool{}
	err := c.db.WithContext(ctx).Model(&model.CostRecord{}).
		Where("occurred_at >= ? AND occurred_at < ?", from, to).
		Select(toolExpr + " AS tool_name, SUM(total_tokens) AS tokens, SUM(estimated_cny) AS cny").
		Group(toolExpr).
		Order("cny DESC").
		Scan(&rows).Error
	return rows, err
}
