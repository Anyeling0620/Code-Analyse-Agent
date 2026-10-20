// Package limiter 提供基于 Redis 的分布式令牌桶限流。
//
// 旧实现把令牌桶放在单进程内存里：多实例部署时每个实例各有一套令牌，实际放行量是
// 「额度 × 实例数」；进程重启还会把额度重置。这里改为 Redis Lua 原子执行——取时间、
// 补令牌、扣令牌、写回在一次脚本调用里完成，多实例共享同一份桶，也不会受各节点
// 时钟漂移影响（时间统一取 Redis 的 TIME）。
//
// 桶参数保持与旧实现一致：容量 = 每分钟允许的突发请求数，按 capacity/window 匀速回填。
// Redis 不可用时按配置降级为进程内令牌桶（默认 fail-open），避免限流组件本身变成单点。
package limiter

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"edu.agent.code/config"
	"edu.agent.code/utils/logger"

	"github.com/redis/go-redis/v9"
)

const (
	// redisCallTimeout 是单次限流判定的 Redis 超时。限流在请求入口，不能被慢 Redis 拖住。
	redisCallTimeout = 500 * time.Millisecond
	// redisWarnInterval 限制 Redis 故障日志的频率，避免故障期间刷屏。
	redisWarnInterval = 30 * time.Second
	// localSweepInterval 是进程内兜底桶的清理间隔。
	localSweepInterval = time.Minute
	// localIdleTTL 是兜底桶在内存里保留的空闲时间。
	localIdleTTL = 10 * time.Minute
)

// tokenBucketScript 是令牌桶的 Lua 实现，返回 {是否放行, 剩余令牌, 重试等待毫秒}。
//
// 用 Redis TIME 而不是客户端时间：多个实例的时钟不可能完全一致，若各自带时间戳进来，
// 回填量会出现负数或重复计算。PEXPIRE 保证桶在一轮窗口后被回收，长期不活跃的 key
// 不会把 Redis 内存吃满（旧实现的内存泄漏问题在 Redis 侧同样要防）。
var tokenBucketScript = redis.NewScript(`
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local window_ms = tonumber(ARGV[2])
local cost = tonumber(ARGV[3])

if capacity <= 0 then
  return {1, 0, 0}
end
if window_ms <= 0 then
  window_ms = 60000
end
if cost <= 0 then
  cost = 1
end

local now_parts = redis.call('TIME')
local now_ms = tonumber(now_parts[1]) * 1000 + math.floor(tonumber(now_parts[2]) / 1000)
local rate = capacity / window_ms

local stored = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(stored[1])
local ts = tonumber(stored[2])
if tokens == nil or ts == nil then
  tokens = capacity
  ts = now_ms
end
if ts > now_ms then
  ts = now_ms
end
if tokens > capacity then
  tokens = capacity
end

tokens = tokens + (now_ms - ts) * rate
if tokens > capacity then
  tokens = capacity
end

local allowed = 0
local retry_after = 0
if cost > capacity then
  retry_after = window_ms
elseif tokens >= cost then
  tokens = tokens - cost
  allowed = 1
else
  retry_after = math.ceil((cost - tokens) / rate)
end

redis.call('HSET', key, 'tokens', string.format('%.6f', tokens), 'ts', string.format('%d', now_ms))
redis.call('PEXPIRE', key, math.max(window_ms + 1000, 60000))

return {allowed, math.floor(tokens), retry_after}
`)

const (
	sourceDisabled   = "disabled"
	sourceRedis      = "redis"
	sourceLocal      = "local"
	sourceFailClosed = "fail_closed"
)

// Decision 是一次限流判定的结果。
type Decision struct {
	Allowed bool
	// Remaining 是判定后桶里剩余的令牌数（向下取整）。
	Remaining int
	// RetryAfter 仅在拒绝时有意义：建议客户端等待多久再重试。
	RetryAfter time.Duration
	// Source 记录判定来自 redis / local / fail_closed，便于排障时区分是否降级。
	Source string
}

// Limiter 是多实例共享的令牌桶限流器。
type Limiter struct {
	rdb   redis.UniversalClient
	cfg   config.RateLimit
	local *localBuckets

	warnedAt atomic.Int64
}

// New 构造限流器。rdb 为 nil 时只使用进程内兜底桶（主要用于测试或未配置 Redis 的场景）。
func New(rdb redis.UniversalClient, cfg config.RateLimit) *Limiter {
	return &Limiter{
		rdb:   rdb,
		cfg:   cfg,
		local: newLocalBuckets(),
	}
}

// Allow 消费一个令牌。
func (l *Limiter) Allow(ctx context.Context, key string, capacity int) Decision {
	return l.AllowN(ctx, key, capacity, 1)
}

// AllowN 消费 cost 个令牌，用于「一次请求等价于多次调用」的场景（如批量检索）。
func (l *Limiter) AllowN(ctx context.Context, key string, capacity, cost int) Decision {
	if l == nil || capacity <= 0 {
		return Decision{Allowed: true, Source: sourceDisabled}
	}
	if cost <= 0 {
		cost = 1
	}
	window := l.cfg.Window()

	if l.rdb != nil {
		decision, err := l.allowRedis(ctx, key, capacity, cost, window)
		if err == nil {
			return decision
		}
		l.warnRedis(err)
	}
	if !l.cfg.IsFailOpen() {
		// 宁可拒绝也不放大流量：Redis 故障时限流必须继续生效。
		return Decision{Allowed: false, RetryAfter: time.Second, Source: sourceFailClosed}
	}
	return l.local.allow(key, capacity, cost, window, time.Now())
}

func (l *Limiter) allowRedis(ctx context.Context, key string, capacity, cost int, window time.Duration) (Decision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, redisCallTimeout)
	defer cancel()

	redisKey := l.cfg.Prefix() + ":" + key
	reply, err := tokenBucketScript.Run(callCtx, l.rdb, []string{redisKey}, capacity, window.Milliseconds(), cost).Slice()
	if err != nil {
		return Decision{}, err
	}
	if len(reply) < 3 {
		return Decision{}, fmt.Errorf("limiter: unexpected script reply %v", reply)
	}
	allowed, _ := reply[0].(int64)
	remaining, _ := reply[1].(int64)
	retryMillis, _ := reply[2].(int64)
	return Decision{
		Allowed:    allowed == 1,
		Remaining:  int(remaining),
		RetryAfter: time.Duration(retryMillis) * time.Millisecond,
		Source:     sourceRedis,
	}, nil
}

func (l *Limiter) warnRedis(err error) {
	now := time.Now().UnixNano()
	last := l.warnedAt.Load()
	if now-last < int64(redisWarnInterval) {
		return
	}
	if !l.warnedAt.CompareAndSwap(last, now) {
		return
	}
	logger.Warn("ratelimit: redis unavailable, fallback to in-process bucket (fail_open=%v): %v", l.cfg.IsFailOpen(), err)
}

// localBuckets 是 Redis 不可用时的进程内兜底实现，与旧实现语义一致，但补上了空闲清理。
type localBuckets struct {
	mu        sync.Mutex
	buckets   map[string]*localBucket
	nextSweep time.Time
}

type localBucket struct {
	capacity int
	tokens   float64
	last     time.Time
}

func newLocalBuckets() *localBuckets {
	return &localBuckets{buckets: make(map[string]*localBucket)}
}

func (b *localBuckets) allow(key string, capacity, cost int, window time.Duration, now time.Time) Decision {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.sweepLocked(now)

	bucket, ok := b.buckets[key]
	if !ok {
		bucket = &localBucket{capacity: capacity, tokens: float64(capacity), last: now}
		b.buckets[key] = bucket
	}
	if bucket.capacity != capacity {
		bucket.capacity = capacity
		if bucket.tokens > float64(capacity) {
			bucket.tokens = float64(capacity)
		}
	}

	rate := float64(capacity) / window.Seconds()
	if elapsed := now.Sub(bucket.last); elapsed > 0 {
		bucket.tokens = math.Min(float64(capacity), bucket.tokens+elapsed.Seconds()*rate)
		bucket.last = now
	}

	if bucket.tokens >= float64(cost) {
		bucket.tokens -= float64(cost)
		return Decision{Allowed: true, Remaining: int(bucket.tokens), Source: sourceLocal}
	}
	missing := float64(cost) - bucket.tokens
	return Decision{
		Allowed:    false,
		Remaining:  int(bucket.tokens),
		RetryAfter: time.Duration(missing / rate * float64(time.Second)),
		Source:     sourceLocal,
	}
}

func (b *localBuckets) sweepLocked(now time.Time) {
	if now.Before(b.nextSweep) {
		return
	}
	b.nextSweep = now.Add(localSweepInterval)
	for key, bucket := range b.buckets {
		if now.Sub(bucket.last) > localIdleTTL {
			delete(b.buckets, key)
		}
	}
}
