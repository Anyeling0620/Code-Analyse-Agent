package api

import (
	"edu.agent.code/common"
	"edu.agent.code/service/auth"
	"edu.agent.code/service/dto"
	"errors"
	"github.com/gin-gonic/gin"
	"strings"
)

func (h *Handler) Login(ctx *gin.Context) {
	var req dto.LoginRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		h.writeResp(ctx, nil, common.ParamError.WithError(err))
		return
	}
	identifier := strings.TrimSpace(req.Email)
	if identifier == "" {
		identifier = strings.TrimSpace(req.Username)
	}
	if identifier == "" || req.Password == "" {
		h.writeResp(ctx, nil, common.MissingCredentials)
		return
	}

	token, user, err := h.auth.LoginWithIP(ctx.Request.Context(), identifier, req.Password, ctx.ClientIP())
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			h.writeResp(ctx, nil, common.AuthFailed)
			return
		}
		if errors.Is(err, auth.ErrLoginTooFrequent) {
			h.writeResp(ctx, nil, common.LoginTooFrequent)
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

// SendEmailCode 发送邮箱注册验证码。该接口公开访问，因此同时做邮箱格式、
// 发送冷却与 IP 频率限制，防止被当成邮件轰炸的跳板。
func (h *Handler) SendEmailCode(ctx *gin.Context) {
	var req dto.EmailCodeRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		h.writeResp(ctx, nil, common.ParamError.WithError(err))
		return
	}

	expiresIn, resendAfter, err := h.auth.SendEmailCode(
		ctx.Request.Context(), req.Email, req.Purpose, ctx.ClientIP(),
	)
	if err != nil {
		h.writeResp(ctx, nil, emailCodeErrno(err))
		return
	}
	h.writeResp(ctx, &dto.EmailCodeResponse{
		ExpiresIn:   int(expiresIn.Seconds()),
		ResendAfter: int(resendAfter.Seconds()),
	}, common.OK)
}

// Register 用邮箱验证码注册并直接返回登录令牌，注册成功即进入工作台。
func (h *Handler) Register(ctx *gin.Context) {
	var req dto.RegisterRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		h.writeResp(ctx, nil, common.ParamError.WithError(err))
		return
	}

	token, user, err := h.auth.Register(ctx.Request.Context(), req.Email, req.Code, req.Password)
	if err != nil {
		h.writeResp(ctx, nil, registerErrno(err))
		return
	}
	h.writeResp(ctx, &dto.LoginResponse{
		Token:  token,
		UserID: user.UserID,
		Plan:   string(user.Plan),
	}, common.OK)
}

// ResetPassword 用邮箱验证码重设密码。重设成功后服务端会拒绝旧令牌，
// 因此这里不直接签发新令牌，由用户使用新密码重新登录。
func (h *Handler) ResetPassword(ctx *gin.Context) {
	var req dto.ResetPasswordRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		h.writeResp(ctx, nil, common.ParamError.WithError(err))
		return
	}
	if err := h.auth.ResetPassword(ctx.Request.Context(), req.Email, req.Code, req.Password); err != nil {
		h.writeResp(ctx, nil, registerErrno(err))
		return
	}
	h.writeResp(ctx, nil, common.OK)
}

// emailCodeErrno 把服务层错误翻译成带中文提示的响应码。
func emailCodeErrno(err error) common.Errno {
	switch {
	case errors.Is(err, auth.ErrRegisterDisabled):
		return common.RegisterDisabled
	case errors.Is(err, auth.ErrPasswordResetDisabled):
		return common.PasswordResetDisabled
	case errors.Is(err, auth.ErrInvalidEmail):
		return common.InvalidEmail
	case errors.Is(err, auth.ErrEmailRegistered):
		return common.EmailRegistered
	case errors.Is(err, auth.ErrSendTooFrequent):
		return common.SendTooFrequent
	case errors.Is(err, auth.ErrUnsupportedScope):
		return common.UnsupportedPurpose
	case errors.Is(err, auth.ErrCodeSendFailed):
		return common.VerificationSendFail
	default:
		return common.ServerError.WithError(err)
	}
}

// registerErrno 把注册错误翻译成带中文提示的响应码。
func registerErrno(err error) common.Errno {
	switch {
	case errors.Is(err, auth.ErrRegisterDisabled):
		return common.RegisterDisabled
	case errors.Is(err, auth.ErrPasswordResetDisabled):
		return common.PasswordResetDisabled
	case errors.Is(err, auth.ErrInvalidEmail):
		return common.InvalidEmail
	case errors.Is(err, auth.ErrEmailRegistered):
		return common.EmailRegistered
	case errors.Is(err, auth.ErrPasswordTooShort):
		return common.PasswordTooShort
	case errors.Is(err, auth.ErrPasswordTooLong):
		return common.PasswordTooLong
	case errors.Is(err, auth.ErrPasswordWeak):
		return common.PasswordWeak
	case errors.Is(err, auth.ErrCodeExpired):
		return common.VerificationExpired
	case errors.Is(err, auth.ErrCodeInvalid):
		return common.VerificationInvalid
	case errors.Is(err, auth.ErrCodeTooMany):
		return common.VerificationTooMany
	default:
		return common.ServerError.WithError(err)
	}
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
