package dto

type Version struct {
	AppName   string `json:"app_name"`
	Version   string `json:"version"`
	GoVersion string `json:"go_version"`
	// GuestLogin 表示服务端是否开放游客登录，前端据此决定是否展示游客入口。
	GuestLogin bool `json:"guest_login"`
}
