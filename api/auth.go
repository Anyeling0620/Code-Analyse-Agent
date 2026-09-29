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

// GuestLogin 免账号签发游客令牌，供他人进站体验。身份由服务端按
// "IP + 浏览器指纹"派生（指纹从 X-Device-Fingerprint 请求头读取），
// 因此不需要用户名密码，也不需要新建账号表。
// 注意：IP 取自 gin 的 ClientIP，反代后面部署时要配置受信代理，否则该值可被伪造；
// 这里只是体验用的成本控制，不承担防滥用职责。
func (h *Handler) GuestLogin(ctx *gin.Context) {
	ip := ctx.ClientIP()
	fingerprint := ctx.GetHeader("X-Device-Fingerprint")
	if ip == "" && fingerprint == "" {
		h.writeResp(ctx, nil, common.ParamError.WithMsg("cannot identify guest"))
		return
	}

	token, user, err := h.auth.GuestLogin(ctx.Request.Context(), ip, fingerprint)
	if err != nil {
		if errors.Is(err, auth.ErrGuestDisabled) {
			h.writeResp(ctx, nil, common.PermissionDenied.WithMsg("游客登录未开启"))
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
