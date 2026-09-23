package limiter

import (
	"sync"
	"time"
)

type bucket struct {
	capacity   int
	tokens     float64
	refillRate float64
	lastRefill time.Time
}

type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

func NewLimiter() *Limiter {
	return &Limiter{
		buckets: make(map[string]*bucket),
	}
}

// Allow TODO 潜在问题：内存泄漏（没有清理机制）。锁粒度（全局锁）。浮点精度。
func (l *Limiter) Allow(key string, capacity int) bool {
	if capacity <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{
			capacity:   capacity,
			tokens:     float64(capacity),
			refillRate: float64(capacity) / 60.0,
			lastRefill: time.Now(),
		}
		l.buckets[key] = b
	} else if b.capacity != capacity {
		b.capacity = capacity
		b.refillRate = float64(b.capacity) / 60.0
	}

	now := time.Now()
	elapsed := now.Sub(b.lastRefill)
	b.tokens += elapsed.Seconds() * b.refillRate
	if b.tokens > float64(b.capacity) {
		b.tokens = float64(b.capacity)
	}
	b.lastRefill = now
	if b.tokens >= 1 {
		b.tokens -= 1
		return true
	}
	return false
}
