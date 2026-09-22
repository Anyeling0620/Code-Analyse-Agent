package cost

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/model"
	"gorm.io/gorm"
	"time"
)

type ICost interface {
	DailyTotal(ctx context.Context, date time.Time) (float64, error)
}

type Cost struct {
	db *gorm.DB
}

func NewCost(adaptor adaptor.IAdaptor) *Cost {
	return &Cost{
		db: adaptor.GetDB(),
	}
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
