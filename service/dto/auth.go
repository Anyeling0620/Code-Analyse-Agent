package dto

// LoginRequest 是账号密码登录请求。
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginResponse 是登录成功后返回的令牌与账号信息。
type LoginResponse struct {
	Token  string `json:"token"`
	UserID string `json:"user_id"`
	Plan   string `json:"plan"`
}
