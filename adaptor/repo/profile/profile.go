package profile

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/model"
	"edu.agent.code/service/do"
	"errors"
	"gorm.io/gorm"
	"time"
)

type IProfile interface {
	GetByUserID(ctx context.Context, userId string) (*do.Profile, error)
	Upsert(ctx context.Context, profile *do.Profile) error
}

type Profile struct {
	db *gorm.DB
}

func NewProfile(adaptor adaptor.IAdaptor) *Profile {
	return &Profile{
		db: adaptor.GetDB(),
	}
}

func (p *Profile) GetByUserID(ctx context.Context, userId string) (*do.Profile, error) {
	var profile model.Profile
	err := p.db.WithContext(ctx).
		Where("user_id = ?", userId).
		First(&profile).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return toDomain(&profile), nil
}

func (p *Profile) Upsert(ctx context.Context, req *do.Profile) error {
	if req == nil {
		return nil
	}
	profile := &model.Profile{
		UserID:           req.UserID,
		AuthSubject:      req.AuthSubject,
		UserType:         req.UserType,
		SkillLevel:       req.SkillLevel,
		GoalType:         req.GoalType,
		PurchasedCourses: req.PurchasedCourses,
		CurrentTopic:     req.CurrentTopic,
		CurrentStage:     req.CurrentStage,
		UpdatedAt:        time.Now(),
	}
	err := p.db.WithContext(ctx).Save(profile).Error
	return err
}

func toDomain(p *model.Profile) *do.Profile {
	if p == nil {
		return nil
	}
	return &do.Profile{
		UserID:           p.UserID,
		AuthSubject:      p.AuthSubject,
		UserType:         p.UserType,
		SkillLevel:       p.SkillLevel,
		GoalType:         p.GoalType,
		PurchasedCourses: append([]string(nil), p.PurchasedCourses...),
		CurrentTopic:     p.CurrentTopic,
		CurrentStage:     p.CurrentStage,
		UpdatedAt:        p.UpdatedAt,
	}
}
