package quota

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/model"
	"errors"
	"gorm.io/gorm"
)

type IQuota interface {
	Increment(ctx context.Context, userID, day string) (int64, error)
	QueryByToday(ctx context.Context, userID, day string) (int64, error)
}

type Quota struct {
	db *gorm.DB
}

func NewQuota(adaptor adaptor.IAdaptor) *Quota {
	return &Quota{
		db: adaptor.GetDB(),
	}
}

func (q *Quota) Increment(ctx context.Context, userID, day string) (int64, error) {
	tx := q.db.WithContext(ctx).
		Exec(`
			insert into quota_usage (user_id, day, count) values (?, ?, 1)
			on conflict (user_id, day) do update set count = count + 1`,
			userID, day)
	if tx.Error != nil { // Check 原为 tx != nil 恒为真，成功时也提前 return 0，导致配额计数永远是 0
		return 0, tx.Error
	}
	return q.QueryByToday(ctx, userID, day)
}

func (q *Quota) QueryByToday(ctx context.Context, userID, day string) (int64, error) {
	var raw model.QuotaUsage
	err := q.db.WithContext(ctx).First(&raw, "user_id = ? AND day = ?", userID, day).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return raw.Count, nil
}
