package api

import (
	"edu.agent.code/common"
	"edu.agent.code/service/dto"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ListSessions(ctx *gin.Context) {
	user := h.authUser(ctx)
	req := &dto.ListSession{}
	if err := ctx.BindQuery(req); err != nil {
		h.writeResp(ctx, nil, common.ParamError.WithError(err))
		return
	}

	resp, err := h.session.ListSession(ctx, user, req)
	if err != nil {
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}
	h.writeResp(ctx, resp, common.OK)
}
func (h *Handler) DeleteSession(ctx *gin.Context) {
	user := h.authUser(ctx)
	sessionID := ctx.Param("session_id")
	err := h.session.DeleteSession(ctx.Request.Context(), user.UserID, sessionID)
	if err != nil {
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}
	h.writeResp(ctx, nil, common.OK)

}
func (h *Handler) GetSessionInfo(ctx *gin.Context) {
	user := h.authUser(ctx)
	req := &dto.GetSessionInfo{}
	if err := ctx.BindQuery(req); err != nil {
		h.writeResp(ctx, nil, common.ParamError.WithError(err))
		return
	}

	resp, err := h.session.GetSessionInfo(ctx.Request.Context(), user.UserID, req)
	if err != nil {
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}

	h.writeResp(ctx, resp, common.OK)
}
