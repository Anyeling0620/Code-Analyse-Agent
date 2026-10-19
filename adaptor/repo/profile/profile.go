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

// DefaultRevisionLimit / MaxRevisionLimit 是历史快照接口的默认与上限条数。
const (
	DefaultRevisionLimit = 20
	MaxRevisionLimit     = 100
)

type IProfile interface {
	GetByUserID(ctx context.Context, userId string) (*do.Profile, error)
	// Upsert 保存当前画像；只有内容实际变化时才写库，并在同一事务里追加一条修订快照。
	// source 用于区分手动保存（api）与对话自动同步（chat）。
	Upsert(ctx context.Context, profile *do.Profile, source string) error
	// ListRevisions 按更新时间倒序返回某用户的画像快照。
	ListRevisions(ctx context.Context, userId string, limit, offset int) ([]do.ProfileRevision, error)
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

// Upsert 以事务方式保存画像并追加快照。
//
// 幂等语义很重要：前端每轮对话都会带上完整画像，如果每次请求都写库，
// updated_at 会被无意义地刷新，「最后一次真正修改时间」就失真了。
// 因此内容与当前值完全一致时直接返回，不写 profiles、也不产生快照。
func (p *Profile) Upsert(ctx context.Context, req *do.Profile, source string) error {
	if req == nil {
		return nil
	}
	if source == "" {
		source = do.ProfileSourceAPI
	}
	return p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.Profile
		err := tx.Where("user_id = ?", req.UserID).First(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		exists := err == nil
		if exists && sameProfile(&existing, req) {
			return nil
		}

		now := time.Now()
		// 把实际写入时间回填给调用方：对话路径随后会把 req 转成 DTO 返回给前端，
		// 不回填的话客户端会看到零值/旧值，界面上的“更新时间”就失真了。
		req.UpdatedAt = now
		profile := &model.Profile{
			UserID:       req.UserID,
			AuthSubject:  req.AuthSubject,
			UserType:     req.UserType,
			SkillLevel:   req.SkillLevel,
			GoalType:     req.GoalType,
			Description:  req.Description,
			CurrentTopic: req.CurrentTopic,
			CurrentStage: req.CurrentStage,
			UpdatedAt:    now,
		}
		if err := tx.Save(profile).Error; err != nil {
			return err
		}
		revision := &model.ProfileRevision{
			UserID:       req.UserID,
			UserType:     req.UserType,
			SkillLevel:   req.SkillLevel,
			GoalType:     req.GoalType,
			Description:  req.Description,
			CurrentTopic: req.CurrentTopic,
			CurrentStage: req.CurrentStage,
			Source:       source,
			UpdatedAt:    now,
		}
		return tx.Create(revision).Error
	})
}

func (p *Profile) ListRevisions(ctx context.Context, userId string, limit, offset int) ([]do.ProfileRevision, error) {
	if limit <= 0 {
		limit = DefaultRevisionLimit
	}
	if limit > MaxRevisionLimit {
		limit = MaxRevisionLimit
	}
	if offset < 0 {
		offset = 0
	}
	var rows []model.ProfileRevision
	err := p.db.WithContext(ctx).
		Where("user_id = ?", userId).
		Order("updated_at DESC").
		Order("id DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]do.ProfileRevision, 0, len(rows))
	for _, row := range rows {
		out = append(out, toRevisionDomain(row))
	}
	return out, nil
}

// sameProfile 比较数据库中当前值与待写入值。忽略 UpdatedAt：
// 它由写入时间决定，不参与“内容是否变化”的判断。
func sameProfile(existing *model.Profile, req *do.Profile) bool {
	return existing.AuthSubject == req.AuthSubject &&
		existing.UserType == req.UserType &&
		existing.SkillLevel == req.SkillLevel &&
		existing.GoalType == req.GoalType &&
		existing.Description == req.Description &&
		existing.CurrentTopic == req.CurrentTopic &&
		existing.CurrentStage == req.CurrentStage
}

func toDomain(p *model.Profile) *do.Profile {
	if p == nil {
		return nil
	}
	return &do.Profile{
		UserID:       p.UserID,
		AuthSubject:  p.AuthSubject,
		UserType:     p.UserType,
		SkillLevel:   p.SkillLevel,
		GoalType:     p.GoalType,
		Description:  p.Description,
		CurrentTopic: p.CurrentTopic,
		CurrentStage: p.CurrentStage,
		UpdatedAt:    p.UpdatedAt,
	}
}

func toRevisionDomain(p model.ProfileRevision) do.ProfileRevision {
	return do.ProfileRevision{
		ID:           p.ID,
		UserID:       p.UserID,
		UserType:     p.UserType,
		SkillLevel:   p.SkillLevel,
		GoalType:     p.GoalType,
		Description:  p.Description,
		CurrentTopic: p.CurrentTopic,
		CurrentStage: p.CurrentStage,
		Source:       p.Source,
		UpdatedAt:    p.UpdatedAt,
	}
}
