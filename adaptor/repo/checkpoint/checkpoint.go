package checkpoint

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/model"
	"errors"
	"gorm.io/gorm"
	"time"
)

type CheckPoint struct {
	db *gorm.DB
}

func NewCheckPoint(adaptor adaptor.IAdaptor) *CheckPoint {
	return &CheckPoint{
		db: adaptor.GetDB(),
	}
}

// 下面两个方法是eino自带的两个checkpoint方法

func (c *CheckPoint) Get(ctx context.Context, checkPointID string) ([]byte, bool, error) {
	var row model.AgentCheckpoint
	err := c.db.WithContext(ctx).Where("checkpoint_id = ?", checkPointID).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return row.Data, true, nil
}

func (c *CheckPoint) Set(ctx context.Context, checkPointID string, checkPointData []byte) error {
	row := &model.AgentCheckpoint{
		CheckpointID: checkPointID,
		Data:         checkPointData,
		UpdatedAt:    time.Now(),
	}
	return c.db.WithContext(ctx).Save(&row).Error
}
