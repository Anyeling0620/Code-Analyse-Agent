package router

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"edu.agent.code/common"
	"edu.agent.code/config"
	"edu.agent.code/utils/limiter"

	"github.com/gin-gonic/gin"
)

// defaultExemptPaths 是限流豁免路径：探活与版本查询会被监控高频轮询，
// 计入限流只会让状态页和真实流量互相干扰。
var defaultExemptPaths = map[string]bool{
	"/healthz":     true,
	"/api/version": true,
}

// RateLimit 是入口全局限流（Redis 令牌桶）。
//
// 已登录用户按 user_id 计数，未登录请求按客户端 IP 计数：校园网/办公网出口常常是
// 同一个 NAT，纯按 IP 计数会让互不相干的用户互相误伤；而登录前的爆破、爬虫又没有
// 身份可用，只能按 IP 挡。聊天接口更细粒度的额度由 Quota 中间件按套餐控制，
// 两者用的 Redis key 不同（user: 与 chat:user:），不重复扣同一个桶。
func RateLimit(l *limiter.Limiter, conf config.RateLimit) gin.HandlerFunc {
	if l == nil || !conf.IsEnabled() {
		return func(c *gin.Context) { c.Next() }
	}
	exempt := make(map[string]bool, len(defaultExemptPaths)+len(conf.ExemptPaths))
	for path := range defaultExemptPaths {
		exempt[path] = true
	}
	for _, path := range conf.ExemptPaths {
		if path = strings.TrimSpace(path); path != "" {
			exempt[path] = true
		}
	}

	capacity := conf.IPCapacity()
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodOptions || exempt[c.FullPath()] {
			c.Next()
			return
		}

		key := "ip:" + clientIP(c)
		if user := authUserFrom(c); user != nil && user.UserID != "" {
			key = "user:" + user.UserID
		}
		decision := l.Allow(c.Request.Context(), key, capacity)
		c.Header("X-RateLimit-Limit", strconv.Itoa(capacity))
		c.Header("X-RateLimit-Remaining", strconv.Itoa(decision.Remaining))
		if !decision.Allowed {
			retry := decision.RetryAfter
			if retry < time.Second {
				retry = time.Second
			}
			c.Header("Retry-After", strconv.Itoa(int(retry.Seconds()+0.5)))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"code": http.StatusTooManyRequests,
				"msg":  "Too Many Requests",
			})
			return
		}
		c.Next()
	}
}

// authUserFrom 读取 Auth 中间件写入的登录用户；未登录返回 nil。
func authUserFrom(c *gin.Context) *common.UserInfo {
	value, ok := c.Get(common.CtxKeyAuthUser)
	if !ok {
		return nil
	}
	user, _ := value.(*common.UserInfo)
	return user
}

// clientIP 取真实客户端 IP。
//
// gin 默认信任所有代理，直接读 X-Forwarded-For 的第一段可以被客户端伪造。
// 本站的入口固定是 nginx：它用 X-Real-IP 写 $remote_addr，XFF 用
// $proxy_add_x_forwarded_for 在末尾追加真实来源，所以两者都比裸 XFF 第一段可信。
func clientIP(c *gin.Context) string {
	if ip := strings.TrimSpace(c.GetHeader("X-Real-IP")); ip != "" {
		return ip
	}
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	if host, _, err := net.SplitHostPort(c.Request.RemoteAddr); err == nil {
		return host
	}
	return c.Request.RemoteAddr
}
