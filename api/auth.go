package api

import (
	"edu.agent.code/common"
	"edu.agent.code/service/auth"
	"edu.agent.code/service/dto"
	"errors"
	"github.com/gin-gonic/gin"
)

func (h *Handler) Login(ctx *gin.Context) {
	var req dto.LoginRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		h.writeResp(ctx, nil, common.ParamError.WithError(err))
		return
	}
	if req.Username == "" || req.Password == "" {
		h.writeResp(ctx, nil, common.ParamError.WithMsg("username and password are required"))
		return
	}

	token, user, err := h.auth.Login(ctx.Request.Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			h.writeResp(ctx, nil, common.AuthFailed)
			return
		}
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}
	h.writeResp(ctx, &dto.LoginResponse{
		Token:  token,
		UserID: user.UserID,
		Plan:   string(user.Plan),
	}, common.OK)
}

// Logout 按当前请求携带的令牌吊销登录态。令牌由 Auth 中间件写入 context，
// 因此这里不需要重复解析请求头。
func (h *Handler) Logout(ctx *gin.Context) {
	token := ctx.GetString(common.CtxKeyAuthToken)
	if token != "" {
		if err := h.auth.Logout(ctx.Request.Context(), token); err != nil {
			h.writeResp(ctx, nil, common.ServerError.WithError(err))
			return
		}
	}
	h.writeResp(ctx, nil, common.OK)
}
