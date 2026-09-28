package router

import (
	"edu.agent.code/common"
	"edu.agent.code/service/auth"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
)

// authWhitelist 是无需登录即可访问的路由。
// 健康检查和版本查询在登录前就会被前端调用，登录接口本身也必须放行。
var authWhitelist = map[string]bool{
	"/healthz":        true,
	"/api/version":    true,
	"/api/auth/login": true,
}

// Auth 校验 Authorization: Bearer <token>，并把登录用户写入 gin.Context，
// 供后续 handler 通过 h.authUser 读取。
func Auth(svc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if authWhitelist[c.FullPath()] {
			c.Next()
			return
		}
		token := bearerToken(c.GetHeader("Authorization"))
		if token == "" {
			abortUnauthorized(c)
			return
		}
		user, err := svc.Verify(c.Request.Context(), token)
		if err != nil {
			abortUnauthorized(c)
			return
		}
		c.Set(common.CtxKeyAuthUser, user)
		c.Set(common.CtxKeyUserID, user.UserID)
		c.Set(common.CtxKeyAuthToken, token)
		c.Next()
	}
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

func abortUnauthorized(c *gin.Context) {
	c.JSON(http.StatusUnauthorized, gin.H{
		"code": http.StatusUnauthorized,
		"msg":  "Please Login",
	})
	c.Abort()
}
