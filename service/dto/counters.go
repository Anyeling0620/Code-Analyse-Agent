package dto

import (
	"context"
	"sync/atomic"
)

// RunCounters 是 run 级、跨中间件的计数器。
//
// 为什么要走 ctx 而不是记在 ChatRunState 上：压缩中间件
// （service/agent/compress）由 Eino 在模型调用前回调，它只能拿到 ctx，
// 拿不到 conversation 层的 runState。ctx 里本来就是 run 级上下文
// （common.WithUserAndSession 也是这么传进去的），所以计数器挂这里。
//
// 用 atomic 而不是普通 int：Eino 不保证中间件回调与消息消费严格在同一个
// goroutine 上，读写同一份计数不该依赖这个假设。
type RunCounters struct {
	compactions atomic.Int64
}

// AddCompaction 记一次上下文压缩。接收者为 nil 时静默忽略，
// 这样未配置计数器的调用路径（例如单测直接构造中间件）不需要额外判空。
func (c *RunCounters) AddCompaction() {
	if c == nil {
		return
	}
	c.compactions.Add(1)
}

// Compactions 返回本轮已发生的压缩次数。
func (c *RunCounters) Compactions() int {
	if c == nil {
		return 0
	}
	return int(c.compactions.Load())
}

type runCountersKey struct{}

// WithRunCounters 把计数器挂到 run 的 ctx 上。
func WithRunCounters(ctx context.Context, counters *RunCounters) context.Context {
	return context.WithValue(ctx, runCountersKey{}, counters)
}

// RunCountersFromContext 取出 run 计数器；未挂载时返回 nil（方法均可安全调用）。
func RunCountersFromContext(ctx context.Context) *RunCounters {
	counters, _ := ctx.Value(runCountersKey{}).(*RunCounters)
	return counters
}
