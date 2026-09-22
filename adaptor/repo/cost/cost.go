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
		CompletionTokens: req.CompletionTokens,
		TotalTokens:      req.PromptTokens + req.CompletionTokens,
		EstimatedCNY:     req.EstimatedCNY,
		OccurredAt:       req.OccurredAt,
	}).Error
}

func (c *Cost) DailyTotal(ctx context.Context, date time.Time) (float64, error) {
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	end := start.Add(24 * time.Hour)

	var total float64
	err := c.db.WithContext(ctx).Model(&model.CostRecord{}).
		Where("occurred_at >= ? AND occurred_at < ?", start, end).
		Select("COALESCE(estimated_cny, 0)").
		Scan(&total).Error
	return total, err
}

func (c *Cost) GroupByUser(ctx context.Context, from, to time.Time) ([]do.CostByUser, error) {
	rows := []do.CostByUser{}
	err := c.db.WithContext(ctx).Model(&model.CostRecord{}).
		Where("occurred_at >= ? AND occurred_at < ?", from, to).
		Select("" +
			"user_id SUM(total_tokens) AS total_tokens, " +
			"SUM(estimated_cny) AS estimated_cny)").
		Group("user_id").
		Order("cny DESC").
		Scan(&rows).Error
	return rows, err
}

func (c *Cost) GroupByTool(ctx context.Context, from, to time.Time) ([]do.CostByTool, error) {
	rows := []do.CostByTool{}
	err := c.db.WithContext(ctx).Model(&model.CostRecord{}).
		Where("occurred_at >= ? AND occurred_at < ?", from, to).
		Select("" +
			"COLLAPSE(NULLIF(tool_name, ''), '(direct)') AS tool_name, " +
			"SUM(total_tokens) AS total_tokens" +
			"SUM(estimated_cny) AS estimated_cny)",
		).
		Group("tool_name").
		Order("cny DESC").
		Scan(&rows).Error
	return rows, err

}
