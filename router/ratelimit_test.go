package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"edu.agent.code/config"
	"edu.agent.code/utils/limiter"

	"github.com/gin-gonic/gin"
)

func newRateLimitEngine(conf config.RateLimit) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(RateLimit(limiter.New(nil, conf), conf))
	engine.GET("/api/demo", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	engine.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	return engine
}

func TestRateLimitRejectsAfterCapacity(t *testing.T) {
	engine := newRateLimitEngine(config.RateLimit{IPPerWindow: 2, WindowSeconds: 60})

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/demo", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求应放行，实际 %d", i+1, rec.Code)
		}
		if got := rec.Header().Get("X-RateLimit-Limit"); got != "2" {
			t.Fatalf("X-RateLimit-Limit = %q, want 2", got)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/demo", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("令牌耗尽后应返回 429，实际 %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 响应应带 Retry-After")
	}
}

func TestRateLimitIsolatesClientsAndExemptsHealthz(t *testing.T) {
	engine := newRateLimitEngine(config.RateLimit{IPPerWindow: 1, WindowSeconds: 60})

	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/demo", nil)
	req.RemoteAddr = "10.0.0.1:1111"
	engine.ServeHTTP(first, req)

	// 同一 IP 已被限流。
	blocked := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/demo", nil)
	req.RemoteAddr = "10.0.0.1:2222"
	engine.ServeHTTP(blocked, req)
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("同 IP 第二次请求应为 429，实际 %d", blocked.Code)
	}

	// 另一个 IP 有自己的桶。
	other := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/demo", nil)
	req.RemoteAddr = "10.0.0.2:1111"
	engine.ServeHTTP(other, req)
	if other.Code != http.StatusOK {
		t.Fatalf("不同 IP 不应互相影响，实际 %d", other.Code)
	}

	// 健康检查永远豁免，供监控高频轮询。
	for i := 0; i < 5; i++ {
		health := httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.RemoteAddr = "10.0.0.1:3333"
		engine.ServeHTTP(health, req)
		if health.Code != http.StatusOK {
			t.Fatalf("健康检查应豁免限流，实际 %d", health.Code)
		}
	}
}

func TestRateLimitDisabledPassesThrough(t *testing.T) {
	disabled := false
	conf := config.RateLimit{Enabled: &disabled, IPPerWindow: 1}
	engine := newRateLimitEngine(conf)
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/demo", nil)
		req.RemoteAddr = "10.0.0.9:1234"
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("关闭限流后应全部放行，实际 %d", rec.Code)
		}
	}
}
