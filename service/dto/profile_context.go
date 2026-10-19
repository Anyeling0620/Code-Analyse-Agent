package dto

import "context"

// profileContextKey 用于把当前请求的用户画像透传给 Agent 执行层。
//
// 为什么需要它：主 Agent 的初始消息里已经带了画像 system 消息，但 Eino 的
// AgentTool 会把子 Agent 的完整聊天历史做一次过滤（system 消息全部丢弃），
// 审批恢复（resume）也只是从 checkpoint 继续，不会重新拼初始消息。
// 把画像放进 Go context 后，各层中间件在每次模型调用前都能拿到「当前最新画像」，
// 不依赖某一条消息是否被保留。
type profileContextKey struct{}

// WithProfile 把画像放入 context。nil 画像表示当前没有可注入的画像。
func WithProfile(ctx context.Context, profile *Profile) context.Context {
	if profile == nil {
		return ctx
	}
	return context.WithValue(ctx, profileContextKey{}, profile)
}

// ProfileFromContext 读取 context 中的画像；没有时返回 nil。
func ProfileFromContext(ctx context.Context) *Profile {
	if ctx == nil {
		return nil
	}
	profile, _ := ctx.Value(profileContextKey{}).(*Profile)
	return profile
}
