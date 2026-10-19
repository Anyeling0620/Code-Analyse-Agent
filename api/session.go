package api

import (
	"edu.agent.code/common"
	"edu.agent.code/service/conversation"
	"edu.agent.code/service/dto"
	"errors"
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
	sessionID := ctx.Query("session_id")
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

// GetMessage 按单条消息返回完整调用事件，供历史页点击“调用详情”时懒加载。
// 鉴权与归属校验都在仓储查询里完成（user_id + session_id + message_id）。
func (h *Handler) GetMessage(ctx *gin.Context) {
	user := h.authUser(ctx)
	req := &dto.GetMessage{}
	if err := ctx.BindQuery(req); err != nil {
		h.writeResp(ctx, nil, common.ParamError.WithError(err))
		return
	}

	resp, err := h.session.GetMessage(ctx.Request.Context(), user.UserID, req)
	if err != nil {
		if errors.Is(err, conversation.ErrMessageNotFound) {
			h.writeResp(ctx, nil, common.NotFound.WithMsg("消息不存在"))
			return
		}
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}
	h.writeResp(ctx, resp, common.OK)
}
