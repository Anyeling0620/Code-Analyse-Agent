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
	OK               = Errno{Code: 200, Msg: "OK"}
	ParamError       = Errno{Code: 400, Msg: "ParamError"}
	AuthError        = Errno{Code: 401, Msg: "AuthError"}
	PermissionDenied = Errno{Code: 403, Msg: "PermissionDenied"}
	NotFound         = Errno{Code: 404, Msg: "NotFound"}
	LimitExceeded    = Errno{Code: 429, Msg: "LimitExceeded"}
	ServerError      = Errno{Code: 500, Msg: "ServerError"}

	DatabaseError = Errno{Code: 10001, Msg: "DatabaseError"}
)
