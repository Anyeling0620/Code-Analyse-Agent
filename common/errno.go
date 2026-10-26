package common

import "errors"

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
	// SessionBusy 是同一会话已有正在执行的回答时再次提交。
	//
	// 用 409（Conflict）而不是 429：这不是限流，而是"与当前资源状态冲突"，
	// 前端可以据此区分"稍后重试"与"这条会话已经有一条在跑"，从而决定是提示用户
	// 等待，还是挂到那条 run 上继续接收（见 GetActiveRun）。Msg 会直接展示给用户。
	SessionBusy = Errno{Code: 409, Msg: "该会话已有正在生成的回答，请等待完成或先停止"}

	DatabaseError = Errno{Code: 10001, Msg: "DatabaseError"}
)

// ErrSessionBusy 是 SessionBusy 的错误哨兵，供 errors.Is 判定。
//
// 用指针是必须的：Errno.Error() 定义在 *Errno 上，值类型本身不实现 error，
// 也就不可能作为 errors.Is 的 target。服务层不直接返回 Errno 值，而是返回
// 包一层的业务错误（带 run_id 等字段）并 Unwrap 到它，这样既保留上下文，
// 又让上层能一行判定"这是会话忙"，翻译成 409。
var ErrSessionBusy = &SessionBusy

// AsErrno 把业务层错误翻译成对外响应码。
//
// 命中已知业务错误时返回对应 Errno（保留 Code/Msg，前端据此区分场景）；
// 未命中返回 (ServerError, false)，由调用方决定是否再包一层 WithError。
// 收敛在这里，是为了让「新增一个业务错误」只改这一处，不必在每个 handler 里写 switch。
func AsErrno(err error) (Errno, bool) {
	if err != nil && errors.Is(err, ErrSessionBusy) {
		return SessionBusy, true
	}
	return ServerError, false
}
