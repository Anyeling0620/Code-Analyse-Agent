package circuitbreaker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Transport 把一个熔断器挂到 HTTP 出站调用上：熔断打开时直接返回错误，不再发起连接。
type Transport struct {
	// Base 是实际的 RoundTripper，为 nil 时使用 http.DefaultTransport。
	Base http.RoundTripper
	// Breaker 为 nil 时退化为 Base 的直接代理。
	Breaker *Breaker
}

// RoundTrip 实现 http.RoundTripper。
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	breaker := t.Breaker
	if breaker == nil {
		return base.RoundTrip(req)
	}

	done, err := breaker.Allow()
	if err != nil {
		return nil, fmt.Errorf("circuitbreaker %s: %w", breaker.Name(), err)
	}

	resp, err := base.RoundTrip(req)
	done(!isFailure(req, resp, err))
	return resp, err
}

// isFailure 判定一次出站调用是否应该计入熔断失败。
//
// 只有「对端有问题」才算失败：网络错误、5xx、429、408。4xx（除 408/429）是调用方
// 参数错误，不能拿它把整个下游熔断；客户端主动取消（用户断流）同理不计入。
func isFailure(req *http.Request, resp *http.Response, err error) bool {
	if err != nil {
		if errors.Is(err, context.Canceled) && req != nil && req.Context().Err() != nil {
			return false
		}
		return true
	}
	if resp == nil {
		return true
	}
	switch {
	case resp.StatusCode >= http.StatusInternalServerError:
		return true
	case resp.StatusCode == http.StatusTooManyRequests:
		return true
	case resp.StatusCode == http.StatusRequestTimeout:
		return true
	default:
		return false
	}
}

// NewHTTPClient 返回带熔断的 HTTP 客户端。熔断按 name 共享，同一个下游要传同一个 name。
// 熔断关闭时（Configure 里 Enabled=false）返回不带 Transport 包装的普通客户端。
func NewHTTPClient(name string, timeout time.Duration) *http.Client {
	client := &http.Client{Timeout: timeout}
	if !options().Enabled {
		return client
	}
	client.Transport = &Transport{Base: http.DefaultTransport, Breaker: BreakerFor(name)}
	return client
}
