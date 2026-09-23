package router

import (
	"edu.agent.code/common"
	"github.com/gin-gonic/gin"
)

const demoUserID = "demo-user"

func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := &common.UserInfo{
			UserID: demoUserID,
			Plan:   common.PlanPro,
		}
		c.Set(common.CtxKeyAuthUser, user)
		c.Set(common.CtxKeyUserID, user.UserID)
		c.Next()
	}
}
