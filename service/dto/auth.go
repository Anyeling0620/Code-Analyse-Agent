package dto

// LoginRequest 是账号密码登录请求。
// Email 与 Username 二选一：Email 非空时优先作为登录标识，Username 继续兼容
// 配置账号（如 Anyeling / visitor）。
type LoginRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginResponse 是登录成功后返回的令牌与账号信息。
type LoginResponse struct {
	Token  string `json:"token"`
	UserID string `json:"user_id"`
	Plan   string `json:"plan"`
}

// EmailCodeRequest 是发送邮箱验证码的请求。
type EmailCodeRequest struct {
	Email   string `json:"email"`
	Purpose string `json:"purpose"`
}

// EmailCodeResponse 是验证码发送成功后的有效期与重发间隔（秒）。
type EmailCodeResponse struct {
	ExpiresIn   int `json:"expires_in"`
	ResendAfter int `json:"resend_after"`
}

// RegisterRequest 是邮箱验证码注册请求。
type RegisterRequest struct {
	Email    string `json:"email"`
	Code     string `json:"code"`
	Password string `json:"password"`
}

// ResetPasswordRequest 是邮箱验证码重设密码请求。
type ResetPasswordRequest struct {
	Email    string `json:"email"`
	Code     string `json:"code"`
	Password string `json:"password"`
}
