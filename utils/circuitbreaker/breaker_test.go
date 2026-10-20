package circuitbreaker

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBreakerOpensAfterConsecutiveFailures(t *testing.T) {
	b := &Breaker{name: "test", opts: Options{FailureThreshold: 3, OpenTimeout: 50 * time.Millisecond, HalfOpenMaxRequests: 1, SuccessThreshold: 2}.withDefaults()}

	for i := 0; i < 3; i++ {
		done, err := b.Allow()
		if err != nil {
			t.Fatalf("第 %d 次允许调用失败：%v", i+1, err)
		}
		done(false)
	}
	if state := b.State(); state != StateOpen {
		t.Fatalf("连续失败 3 次后应为 open，实际 %s", state)
	}
	if _, err := b.Allow(); !errors.Is(err, ErrOpen) {
		t.Fatalf("打开状态应返回 ErrOpen，实际 %v", err)
	}

	time.Sleep(60 * time.Millisecond)
	if state := b.State(); state != StateHalfOpen {
		t.Fatalf("超时后应进入 half-open，实际 %s", state)
	}

	done, err := b.Allow()
	if err != nil {
		t.Fatalf("半开状态应允许试探请求：%v", err)
	}
	if _, err := b.Allow(); !errors.Is(err, ErrTooManyRequests) {
		t.Fatalf("半开状态并发试探应被限制，实际 %v", err)
	}
	done(true)
	done2, err := b.Allow()
	if err != nil {
		t.Fatalf("第二次试探应允许：%v", err)
	}
	done2(true)
	if state := b.State(); state != StateClosed {
		t.Fatalf("连续成功 2 次后应恢复 closed，实际 %s", state)
	}
}

func TestBreakerFailureInHalfOpenReopens(t *testing.T) {
	b := &Breaker{name: "test", opts: Options{FailureThreshold: 1, OpenTimeout: 20 * time.Millisecond, HalfOpenMaxRequests: 1, SuccessThreshold: 2}.withDefaults()}
	done, _ := b.Allow()
	done(false)
	time.Sleep(30 * time.Millisecond)
	done, err := b.Allow()
	if err != nil {
		t.Fatalf("半开应放行试探请求：%v", err)
	}
	done(false)
	if state := b.State(); state != StateOpen {
		t.Fatalf("半开失败应重新打开，实际 %s", state)
	}
}

func TestTransportCountsServerErrorsOnly(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/bad" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	b := &Breaker{name: "http-test", opts: Options{FailureThreshold: 2, OpenTimeout: time.Minute, HalfOpenMaxRequests: 1, SuccessThreshold: 1}.withDefaults()}
	client := &http.Client{Transport: &Transport{Base: srv.Client().Transport, Breaker: b}}

	for i := 0; i < 3; i++ {
		resp, err := client.Get(srv.URL + "/client-error")
		if err != nil {
			t.Fatalf("第 %d 次 4xx 请求出错：%v", i+1, err)
		}
		resp.Body.Close()
	}
	if state := b.State(); state != StateClosed {
		t.Fatalf("4xx 不应触发熔断，实际 %s", state)
	}

	for i := 0; i < 2; i++ {
		resp, err := client.Get(srv.URL + "/bad")
		if err != nil {
			t.Fatalf("第 %d 次 5xx 请求出错：%v", i+1, err)
		}
		resp.Body.Close()
	}
	before := hits
	if state := b.State(); state != StateOpen {
		t.Fatalf("连续 2 次 5xx 应触发熔断，实际 %s", state)
	}
	if _, err := client.Get(srv.URL + "/ok"); !errors.Is(err, ErrOpen) {
		t.Fatalf("熔断打开后应直接失败，实际 %v", err)
	}
	if hits != before {
		t.Fatal("熔断打开后不应再打到下游")
	}
}
