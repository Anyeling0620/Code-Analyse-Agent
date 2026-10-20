// Package circuitbreaker 提供进程内熔断器，以及把熔断挂到 HTTP 出站调用上的 RoundTripper。
//
// 项目里 LLM、embedding、rerank、查询改写都是同步出站 HTTP：对端一旦整体不可用，
// 每个请求都会卡到超时再失败，既拖垮连接池也让上游持续承压。熔断器在连续失败后
// 直接短路，给对端留出恢复时间，也把失败从「每个请求等 30s」变成「立即失败」。
package circuitbreaker

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"edu.agent.code/utils/logger"
)

// State 是熔断器状态。
type State int

const (
	// StateClosed 正常放行。
	StateClosed State = iota
	// StateOpen 直接拒绝，等待 OpenTimeout 后进入半开。
	StateOpen
	// StateHalfOpen 试探性放行少量请求，用结果决定是否恢复。
	StateHalfOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

var (
	// ErrOpen 表示熔断器处于打开状态，请求被直接拒绝。
	ErrOpen = errors.New("circuitbreaker: circuit is open")
	// ErrTooManyRequests 表示半开状态下试探请求已满。
	ErrTooManyRequests = errors.New("circuitbreaker: too many requests in half-open state")
)

const (
	defaultFailureThreshold     = 5
	defaultOpenTimeout          = 30 * time.Second
	defaultHalfOpenMaxRequests  = 1
	defaultHalfOpenSuccessCount = 2
)

// Options 是熔断策略。零值可用，缺省项会被补成默认值。
type Options struct {
	// Enabled 为 false 时 NewHTTPClient 返回普通 client，不做任何熔断。
	Enabled bool
	// FailureThreshold 是连续失败多少次后打开熔断（默认 5）。
	FailureThreshold int
	// OpenTimeout 是打开状态持续多久后进入半开（默认 30s）。
	OpenTimeout time.Duration
	// HalfOpenMaxRequests 是半开状态同时允许的试探请求数（默认 1）。
	HalfOpenMaxRequests int
	// SuccessThreshold 是半开状态下连续成功多少次后恢复到闭合（默认 2）。
	SuccessThreshold int
}

func (o Options) withDefaults() Options {
	if o.FailureThreshold <= 0 {
		o.FailureThreshold = defaultFailureThreshold
	}
	if o.OpenTimeout <= 0 {
		o.OpenTimeout = defaultOpenTimeout
	}
	if o.HalfOpenMaxRequests <= 0 {
		o.HalfOpenMaxRequests = 1
	}
	if o.SuccessThreshold <= 0 {
		o.SuccessThreshold = defaultHalfOpenSuccessCount
	}
	return o
}

// Counts 是熔断器统计。
type Counts struct {
	Requests             uint64
	TotalSuccesses       uint64
	TotalFailures        uint64
	ConsecutiveSuccesses uint64
	ConsecutiveFailures  uint64
}

var (
	optionsMu      sync.RWMutex
	currentOptions = Options{Enabled: true}.withDefaults()
	breakers       = make(map[string]*Breaker)
)

// Configure 设置全局熔断策略。启动时调用一次即可；已创建的熔断器会同步更新策略。
func Configure(opts Options) {
	opts = opts.withDefaults()

	optionsMu.Lock()
	currentOptions = opts
	all := make([]*Breaker, 0, len(breakers))
	for _, b := range breakers {
		all = append(all, b)
	}
	optionsMu.Unlock()

	for _, b := range all {
		b.mu.Lock()
		b.opts = opts
		b.mu.Unlock()
	}
}

func options() Options {
	optionsMu.RLock()
	defer optionsMu.RUnlock()
	return currentOptions
}

// BreakerFor 返回按名字共享的熔断器：同一个下游（如 LLM 端点）在进程内只应有一份状态，
// 否则每个 client 各算各的失败次数，熔断永远触发不了。
func BreakerFor(name string) *Breaker {
	optionsMu.Lock()
	defer optionsMu.Unlock()
	if b, ok := breakers[name]; ok {
		return b
	}
	b := &Breaker{name: name, opts: currentOptions}
	breakers[name] = b
	return b
}

// Breaker 是一个三态熔断器，可安全并发使用。
type Breaker struct {
	name string

	mu               sync.Mutex
	state            State
	generation       uint64
	counts           Counts
	expiry           time.Time
	halfOpenInFlight int
	halfOpenSuccess  int
	opts             Options
}

// Name 返回熔断器名字。
func (b *Breaker) Name() string {
	if b == nil {
		return ""
	}
	return b.name
}

// State 返回当前状态。
func (b *Breaker) State() State {
	if b == nil {
		return StateClosed
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentStateLocked(time.Now())
}

// Counts 返回统计快照。
func (b *Breaker) Counts() Counts {
	if b == nil {
		return Counts{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.counts
}

// Allow 申请一次调用许可。返回的 done 必须在调用结束后执行，success 表示这次调用是否成功；
// 忘记调用 done 会让半开状态的试探名额一直被占用。
func (b *Breaker) Allow() (done func(success bool), err error) {
	if b == nil {
		return func(bool) {}, nil
	}
	generation, err := b.beforeRequest()
	if err != nil {
		return nil, err
	}
	return func(success bool) {
		if success {
			b.afterSuccess(generation)
			return
		}
		b.afterFailure(generation)
	}, nil
}

// Execute 在熔断保护下执行 fn。
func (b *Breaker) Execute(fn func() error) error {
	done, err := b.Allow()
	if err != nil {
		return fmt.Errorf("%s: %w", b.Name(), err)
	}
	err = fn()
	done(err == nil)
	return err
}

func (b *Breaker) beforeRequest() (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	state := b.currentStateLocked(now)
	switch state {
	case StateOpen:
		return 0, ErrOpen
	case StateHalfOpen:
		if b.halfOpenInFlight >= b.opts.HalfOpenMaxRequests {
			return 0, ErrTooManyRequests
		}
		b.halfOpenInFlight++
	}
	b.counts.Requests++
	return b.generation, nil
}

func (b *Breaker) afterSuccess(generation uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// generation 不一致说明请求发出后熔断器已经状态迁移，这次结果不能用来判断新状态。
	if generation != b.generation {
		return
	}
	b.counts.TotalSuccesses++
	b.counts.ConsecutiveSuccesses++
	b.counts.ConsecutiveFailures = 0

	if b.state != StateHalfOpen {
		return
	}
	if b.halfOpenInFlight > 0 {
		b.halfOpenInFlight--
	}
	b.halfOpenSuccess++
	if b.halfOpenSuccess >= b.opts.SuccessThreshold {
		b.transitionLocked(StateClosed, time.Now())
	}
}

func (b *Breaker) afterFailure(generation uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if generation != b.generation {
		return
	}
	b.counts.TotalFailures++
	b.counts.ConsecutiveFailures++
	b.counts.ConsecutiveSuccesses = 0

	if b.state == StateHalfOpen {
		if b.halfOpenInFlight > 0 {
			b.halfOpenInFlight--
		}
		b.transitionLocked(StateOpen, time.Now())
		return
	}
	if b.state == StateClosed && int(b.counts.ConsecutiveFailures) >= b.opts.FailureThreshold {
		b.transitionLocked(StateOpen, time.Now())
	}
}

func (b *Breaker) currentStateLocked(now time.Time) State {
	if b.state == StateOpen && !now.Before(b.expiry) {
		b.transitionLocked(StateHalfOpen, now)
	}
	return b.state
}

func (b *Breaker) transitionLocked(next State, now time.Time) {
	prev := b.state
	if prev == next {
		return
	}
	b.state = next
	b.generation++
	b.counts = Counts{}
	b.halfOpenInFlight = 0
	b.halfOpenSuccess = 0
	switch next {
	case StateOpen:
		b.expiry = now.Add(b.opts.OpenTimeout)
		logger.Warn("circuitbreaker: %s opened, requests will fail fast for %s", b.name, b.opts.OpenTimeout)
	case StateHalfOpen:
		logger.Info("circuitbreaker: %s half-open, probing downstream", b.name)
	case StateClosed:
		b.expiry = time.Time{}
		logger.Info("circuitbreaker: %s closed, downstream recovered", b.name)
	}
}
