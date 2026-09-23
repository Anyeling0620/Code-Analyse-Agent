package router

import (
	"edu.agent.code/api"
	"github.com/gin-gonic/gin"
)

func New(h *api.Handler) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	app := gin.New()
	app.Use(gin.Recovery(), gin.Logger(), Tracing())

	routeRegister(app, h)
	return app
}

func routeRegister(app *gin.Engine, h *api.Handler) {
	app.GET("/healthz", h.Health)
	app.GET("/api/cost/daily", h.CostDaily)

	chatQuota := Quota(h.GetQuotaService(), nil)
	chatRoot := app.Group("/api/chat", chatQuota)
	chatRoot.POST("/completion", h.ChatCompletion)
	chatRoot.POST("/stream", h.ChatStream)
	chatRoot.POST("/resume", h.ChatResume)
}
