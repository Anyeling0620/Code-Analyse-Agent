package limiter

import (
	"context"
	"os"
	"testing"
	"time"

	"edu.agent.code/config"

	"github.com/redis/go-redis/v9"
)

func TestAllowFallsBackToLocalBucket(t *testing.T) {
	l := New(nil, config.RateLimit{})
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if got := l.Allow(ctx, "user:1", 3); !got.Allowed {
			t.Fatalf("第 %d 次应放行，实际被拒：%+v", i+1, got)
		}
	}
	denied := l.Allow(ctx, "user:1", 3)
	if denied.Allowed {
		t.Fatal("令牌耗尽后应拒绝")
	}
	if denied.RetryAfter <= 0 {
		t.Fatalf("拒绝时应给出 RetryAfter，实际 %s", denied.RetryAfter)
	}
	if denied.Source != sourceLocal {
		t.Fatalf("Source = %s, want %s", denied.Source, sourceLocal)
	}
}

func TestAllowRefillsOverTime(t *testing.T) {
	l := New(nil, config.RateLimit{WindowSeconds: 1})
	ctx := context.Background()

	if !l.Allow(ctx, "k", 2).Allowed || !l.Allow(ctx, "k", 2).Allowed {
		t.Fatal("前两次应放行")
	}
	if l.Allow(ctx, "k", 2).Allowed {
		t.Fatal("第三次应被拒绝")
	}
	time.Sleep(600 * time.Millisecond)
	if !l.Allow(ctx, "k", 2).Allowed {
		t.Fatal("等待半个窗口后应回填至少一个令牌")
	}
}

func TestAllowCapacityZeroAlwaysAllows(t *testing.T) {
	l := New(nil, config.RateLimit{})
	if !l.Allow(context.Background(), "k", 0).Allowed {
		t.Fatal("capacity<=0 表示不限流，应放行")
	}
}

func TestAllowNConsumesMultipleTokens(t *testing.T) {
	l := New(nil, config.RateLimit{})
	ctx := context.Background()

	if !l.AllowN(ctx, "k", 5, 3).Allowed {
		t.Fatal("容量 5 时消费 3 个令牌应放行")
	}
	if l.AllowN(ctx, "k", 5, 3).Allowed {
		t.Fatal("只剩 2 个令牌时消费 3 个应被拒绝")
	}
}

func TestLocalBucketsSweepIdleEntries(t *testing.T) {
	l := New(nil, config.RateLimit{})
	now := time.Now()
	l.local.nextSweep = now.Add(-time.Second)
	l.local.buckets["stale"] = &localBucket{capacity: 1, tokens: 1, last: now.Add(-time.Hour)}

	l.local.allow("fresh", 1, 1, time.Minute, now)
	if _, ok := l.local.buckets["stale"]; ok {
		t.Fatal("空闲超过 TTL 的桶应被清理")
	}
}

// TestRedisTokenBucket 需要真实 Redis：设置 ANALYSE_TEST_REDIS_ADDR 后才会执行。
// 例：ANALYSE_TEST_REDIS_ADDR=127.0.0.1:6379 go test ./utils/limiter -run Redis -v
func TestRedisTokenBucket(t *testing.T) {
	addr := os.Getenv("ANALYSE_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("未设置 ANALYSE_TEST_REDIS_ADDR，跳过 Redis 集成测试")
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		DB:       15,
		Username: os.Getenv("ANALYSE_TEST_REDIS_USERNAME"),
		Password: os.Getenv("ANALYSE_TEST_REDIS_PASSWORD"),
	})
	defer rdb.Close()
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis ping: %v", err)
	}
	key := "test:" + time.Now().Format("150405.000000")
	defer rdb.Del(ctx, "analyse:ratelimit:"+key)

	l := New(rdb, config.RateLimit{KeyPrefix: "analyse:ratelimit", WindowSeconds: 60})
	for i := 0; i < 3; i++ {
		if got := l.Allow(ctx, key, 3); !got.Allowed || got.Source != sourceRedis {
			t.Fatalf("第 %d 次应经 Redis 放行，实际 %+v", i+1, got)
		}
	}
	denied := l.Allow(ctx, key, 3)
	if denied.Allowed {
		t.Fatal("令牌耗尽后应拒绝")
	}
	if denied.RetryAfter <= 0 {
		t.Fatalf("拒绝时应给出 RetryAfter，实际 %s", denied.RetryAfter)
	}
	if ttl := rdb.PTTL(ctx, "analyse:ratelimit:"+key).Val(); ttl <= 0 {
		t.Fatalf("限流 key 应带 TTL，实际 %s", ttl)
	}
}
