package router

import (
	"edu.agent.code/api"
	"github.com/gin-gonic/gin"
)

func New(h *api.Handler) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	app := gin.New()
	app.Use(gin.Recovery(), gin.Logger(), Tracing(), Auth(h.GetAuthService()))

	routeRegister(app, h)
	return app
}

func routeRegister(app *gin.Engine, h *api.Handler) {
	app.GET("/healthz", h.Health)
	app.GET("/api/version", h.Version)
	app.POST("/api/auth/login", h.Login)                     // 账号密码登录，换取令牌
	app.POST("/api/auth/logout", h.Logout)                   // 吊销当前令牌
	app.GET("api/quota/today", h.QuotaToday)                 // 用户当天调用量
	app.GET("/api/cost/daily", h.CostDaily)                  // 用户当天的成本
	app.GET("/api/cost/by_user", h.CostByUser)               // 按用户统计成本
	app.GET("/api/cost/by_tool", h.CostByTool)               // 按工具统计成本
	app.GET("/api/sessions", h.ListSessions)                 // 获取会话列表
	app.GET("/api/sessions/info", h.GetSessionInfo)          // 获取会话详情
	app.DELETE("/api/sessions/delete", h.DeleteSession)      // 删除会话
	app.POST("/api/sessions/share", h.CreateSessionShare)    // 创建只读分享
	app.DELETE("/api/sessions/share", h.RevokeSessionShare)  // 撤销只读分享
	app.GET("/api/sessions/shared/info", h.GetSharedSession) // 读取只读分享（无需登录）

	app.POST("/api/rag/retriever", h.Retriever)
	app.GET("/api/rag/project_status", h.ProjectIndexStatus) // 项目代码语义索引构建状态

	chatQuota := Quota(h.GetQuotaService(), nil)
	chatRoot := app.Group("/api/chat", chatQuota)
	chatRoot.POST("/completion", h.ChatCompletion) //阻塞等待一次性输出
	chatRoot.POST("/stream", h.ChatStream)         // sse流式输出
	chatRoot.GET("/stream/run", h.ChatStreamRun)   // 断连续传：按 Last-Event-ID 回放并继续跟随
	chatRoot.GET("/run/active", h.ChatRunActive)   // 查询会话下仍可续跑的 run
	chatRoot.POST("/resume", h.ChatResume)         // 中断等待审批之后进行恢复流式
}
