package api

import (
	"edu.agent.code/common"
	"edu.agent.code/service/conversation"
	"edu.agent.code/service/dto"
	"errors"
	"github.com/gin-gonic/gin"
	"strings"
)

// CreateSessionShare 为当前登录用户自己的会话创建只读分享。
func (h *Handler) CreateSessionShare(ctx *gin.Context) {
	user := h.authUser(ctx)
	req := &dto.CreateShareRequest{}
	if err := ctx.ShouldBindJSON(req); err != nil {
		h.writeResp(ctx, nil, common.ParamError.WithError(err))
		return
	}

	resp, err := h.session.CreateShare(ctx.Request.Context(), user.UserID, req.SessionID)
	if err != nil {
		if errors.Is(err, conversation.ErrShareSessionNotFound) {
			h.writeResp(ctx, nil, common.SessionNotFound)
			return
		}
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}
	h.writeResp(ctx, resp, common.OK)
}

// GetSharedSession 读取只读分享快照。该路由在鉴权白名单里，未登录也能访问。
func (h *Handler) GetSharedSession(ctx *gin.Context) {
	req := &dto.GetSharedSession{}
	if err := ctx.BindQuery(req); err != nil {
		h.writeResp(ctx, nil, common.ParamError.WithError(err))
		return
	}
	if strings.TrimSpace(req.ShareToken) == "" {
		h.writeResp(ctx, nil, common.ShareLinkInvalid)
		return
	}

	resp, err := h.session.GetSharedSession(ctx.Request.Context(), req.ShareToken)
	if err != nil {
		if errors.Is(err, conversation.ErrShareLinkInvalid) {
			h.writeResp(ctx, nil, common.ShareLinkInvalid)
			return
		}
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}
	h.writeResp(ctx, resp, common.OK)
}

// RevokeSessionShare 撤销当前用户某个会话下所有未失效的只读分享。
func (h *Handler) RevokeSessionShare(ctx *gin.Context) {
	user := h.authUser(ctx)
	sessionID := ctx.Query("session_id")
	if err := h.session.RevokeShare(ctx.Request.Context(), user.UserID, sessionID); err != nil {
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}
	h.writeResp(ctx, nil, common.OK)
}
