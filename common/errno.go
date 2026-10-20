package common

type Errno struct {
	Code   int    `json:"code"`
	Msg    string `json:"msg"`
	ErrMsg string `json:"err_msg"`
}

func (e *Errno) Error() string {
	if e.ErrMsg != "" {
		return e.Msg + ":" + e.ErrMsg
	}
	return e.Msg
}

func (e Errno) WithError(err error) Errno {
	if err != nil {
		e.ErrMsg = err.Error()
	}
	return e
}

func (e Errno) WithMsg(msg string) Errno {
	if msg != "" {
		e.ErrMsg = msg
	}
	return e
}

var (
	OK         = Errno{Code: 200, Msg: "OK"}
	ParamError = Errno{Code: 400, Msg: "ParamError"}
	AuthError  = Errno{Code: 401, Msg: "AuthError"}
	// AuthFailed 是登录凭据错误。邮箱用户与配置账号返回同一提示，
	// 不暴露「该邮箱是否已注册」。Msg 会直接展示在登录框，故不追加 ErrMsg 前缀。
	AuthFailed       = Errno{Code: 401, Msg: "邮箱/账号或密码错误"}
	PermissionDenied = Errno{Code: 403, Msg: "PermissionDenied"}
	NotFound         = Errno{Code: 404, Msg: "NotFound"}
	LimitExceeded    = Errno{Code: 429, Msg: "LimitExceeded"}
	ServerError      = Errno{Code: 500, Msg: "ServerError"}

	// 邮箱注册相关错误。Msg 会直接展示给用户，因此不再追加 ErrMsg 前缀。
	EmailRegistered       = Errno{Code: 409, Msg: "该邮箱已注册，请直接登录"}
	RegisterDisabled      = Errno{Code: 403, Msg: "邮箱注册未开启"}
	PasswordResetDisabled = Errno{Code: 403, Msg: "密码重置未开启"}
	VerificationExpired   = Errno{Code: 400, Msg: "验证码已过期，请重新获取"}
	VerificationInvalid   = Errno{Code: 400, Msg: "验证码错误"}
	VerificationTooMany   = Errno{Code: 400, Msg: "验证码错误次数过多，请重新获取"}
	VerificationSendFail  = Errno{Code: 500, Msg: "验证码发送失败，请稍后重试"}
	SendTooFrequent       = Errno{Code: 429, Msg: "发送过于频繁，请稍后再试"}
	LoginTooFrequent      = Errno{Code: 429, Msg: "登录失败次数过多，请稍后再试"}
	InvalidEmail          = Errno{Code: 400, Msg: "邮箱格式不正确"}
	UnsupportedPurpose    = Errno{Code: 400, Msg: "不支持的验证码用途"}
	MissingCredentials    = Errno{Code: 400, Msg: "请输入邮箱/账号和密码"}
	PasswordTooShort      = Errno{Code: 400, Msg: "密码长度至少 8 个字符"}
	PasswordTooLong       = Errno{Code: 400, Msg: "密码长度不能超过 72 个字符"}
	PasswordWeak          = Errno{Code: 400, Msg: "密码必须同时包含字母和数字"}

	// SessionNotFound 是会话不存在或不属于当前用户（只读分享创建时使用）。
	SessionNotFound = Errno{Code: 400, Msg: "会话不存在"}
	// ShareLinkInvalid 是分享令牌不存在、已撤销或已过期。
	ShareLinkInvalid = Errno{Code: 400, Msg: "分享链接无效或已失效"}

	DatabaseError = Errno{Code: 10001, Msg: "DatabaseError"}
)
