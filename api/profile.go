package api

import (
	"errors"
	"strconv"
	"strings"

	"edu.agent.code/adaptor/repo/profile"
	"edu.agent.code/common"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"

	"github.com/gin-gonic/gin"
)

// descriptionMaxRunes 与前端 textarea 的 maxLength 对齐。描述是要拼进每轮 system 消息的
// 整段背景，太长既占上下文也没人读完，所以服务端留一道兜底截断。
const descriptionMaxRunes = 1000

// GetProfile 读取当前登录用户的画像。没有记录时返回空画像而不是 404：
// 「还没填过」是正常状态，前端登录后要用它来回填强引导表单。
func (h *Handler) GetProfile(ctx *gin.Context) {
	user := h.authUser(ctx)
	profile, err := h.profiles.GetByUserID(ctx.Request.Context(), user.UserID)
	if err != nil {
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}
	h.writeResp(ctx, profileToDTO(user.UserID, profile), common.OK)
}

// SaveProfile 保存当前登录用户的画像（全量替换）。
//
// 归属只取登录令牌里的 user_id：请求体不带 user_id，否则任何登录用户都能覆盖别人的画像。
// 保存成功后回读一次再返回，避免「前端以为存了什么」和库里实际内容不一致。
func (h *Handler) SaveProfile(ctx *gin.Context) {
	user := h.authUser(ctx)
	req := &dto.ProfileUpdateReq{}
	if err := ctx.ShouldBindJSON(req); err != nil {
		h.writeResp(ctx, nil, common.ParamError.WithError(err))
		return
	}

	// 先读旧值：auth_subject 是客户端看不到的字段，不能因为一次保存被清空。
	existing, err := h.profiles.GetByUserID(ctx.Request.Context(), user.UserID)
	if err != nil {
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}

	profile, err := buildProfileUpdate(user.UserID, req, existing)
	if err != nil {
		h.writeResp(ctx, nil, common.ParamError.WithError(err))
		return
	}
	if err := h.profiles.Upsert(ctx.Request.Context(), &profile, do.ProfileSourceAPI); err != nil {
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}

	saved, err := h.profiles.GetByUserID(ctx.Request.Context(), user.UserID)
	if err != nil {
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}
	h.writeResp(ctx, profileToDTO(user.UserID, saved), common.OK)
}

// GetProfileHistory 返回当前登录用户最近的画像快照。
//
// limit 默认 20、上限 100；offset 用于分页。响应只包含当前登录用户的数据，
// 与 GET /api/profile 一样，归属完全取自令牌。
func (h *Handler) GetProfileHistory(ctx *gin.Context) {
	user := h.authUser(ctx)
	limit := profile.DefaultRevisionLimit
	offset := 0
	if raw := ctx.Query("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > profile.MaxRevisionLimit {
		limit = profile.MaxRevisionLimit
	}
	if raw := ctx.Query("offset"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	revisions, err := h.profiles.ListRevisions(ctx.Request.Context(), user.UserID, limit, offset)
	if err != nil {
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}
	items := make([]*dto.ProfileRevision, 0, len(revisions))
	for _, revision := range revisions {
		items = append(items, revisionToDTO(revision))
	}
	h.writeResp(ctx, &dto.ProfileHistory{
		Items:  items,
		Limit:  limit,
		Offset: offset,
	}, common.OK)
}

func revisionToDTO(revision do.ProfileRevision) *dto.ProfileRevision {
	return &dto.ProfileRevision{
		ID:           revision.ID,
		UserID:       revision.UserID,
		UserType:     revision.UserType,
		SkillLevel:   revision.SkillLevel,
		GoalType:     revision.GoalType,
		Description:  revision.Description,
		CurrentTopic: revision.CurrentTopic,
		CurrentStage: revision.CurrentStage,
		Source:       revision.Source,
		UpdatedAt:    revision.UpdatedAt,
	}
}

// buildProfileUpdate 把请求体归一化为可落库的画像：去空白、截断描述、保留旧行的
// auth_subject。全空请求直接拒绝——PUT 是全量替换，放过去等于清空画像。
func buildProfileUpdate(userID string, req *dto.ProfileUpdateReq, existing *do.Profile) (do.Profile, error) {
	profile := do.Profile{
		UserID:       userID,
		UserType:     strings.TrimSpace(req.UserType),
		SkillLevel:   strings.TrimSpace(req.SkillLevel),
		GoalType:     strings.TrimSpace(req.GoalType),
		Description:  strings.TrimSpace(req.Description),
		CurrentTopic: strings.TrimSpace(req.CurrentTopic),
		CurrentStage: strings.TrimSpace(req.CurrentStage),
	}
	if profile.UserType == "" && profile.SkillLevel == "" && profile.GoalType == "" &&
		profile.Description == "" && profile.CurrentTopic == "" && profile.CurrentStage == "" {
		return do.Profile{}, errors.New("画像内容不能全为空")
	}
	if existing != nil {
		profile.AuthSubject = existing.AuthSubject
	}
	if runes := []rune(profile.Description); len(runes) > descriptionMaxRunes {
		profile.Description = string(runes[:descriptionMaxRunes])
	}
	return profile, nil
}

// profileToDTO 把领域对象转成出参；库里没有记录时返回 user_id 已填、其余为空的画像。
func profileToDTO(userID string, profile *do.Profile) *dto.Profile {
	if profile == nil {
		return &dto.Profile{UserID: userID}
	}
	return &dto.Profile{
		UserID:       userID,
		AuthSubject:  profile.AuthSubject,
		UserType:     profile.UserType,
		SkillLevel:   profile.SkillLevel,
		GoalType:     profile.GoalType,
		Description:  profile.Description,
		CurrentTopic: profile.CurrentTopic,
		CurrentStage: profile.CurrentStage,
		UpdatedAt:    profile.UpdatedAt,
	}
}
