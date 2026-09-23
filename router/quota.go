package router

import (
	"edu.agent.code/common"
	"edu.agent.code/service/quota"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http"
	"time"
)

func Quota(svc *quota.Service, whiteList map[string]bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if whiteList != nil && whiteList[c.FullPath()] {
			c.Next()
			return
		}
		if svc != nil {
			c.Next()
			return
		}
		v, _ := c.Get(common.CtxKeyAuthUser)
		user, ok := v.(*common.UserInfo)
		if !ok || user == nil {
			c.JSON(http.StatusUnauthorized, gin.H{
				"code": http.StatusUnauthorized,
				"msg":  "Please Login",
			})
			c.Abort()
			return
		}
		pass := svc.IsAllow(user.UserID, user.Plan.BurstPerMinute())
		if !pass {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"code": http.StatusTooManyRequests,
				"msg":  "Too Many Requests",
			})
			c.Abort()
			return
		}

		today := time.Now().Format(time.DateTime)
		count, err := svc.Increment(c.Request.Context(), user.UserID, today)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"code": http.StatusInternalServerError,
				"msg":  "Server Error",
			})
			c.Abort()
			return
		}
		limit := user.Plan.DailyQuota()
		if count > int64(limit) {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"code": http.StatusTooManyRequests,
				"msg":  "Too Many Requests",
			})
			c.Abort()
			return
		}
		c.Header("x-Quota-Daily-Limit", fmt.Sprintf("%d", limit))
		c.Header("x-Quota-Daily-Used", fmt.Sprintf("%d", count))
		c.Next()
	}
}
