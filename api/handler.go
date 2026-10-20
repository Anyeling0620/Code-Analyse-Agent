package api

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/profile"
	"edu.agent.code/common"
	"edu.agent.code/config"
	"edu.agent.code/service/auth"
	"edu.agent.code/service/codeindex"
	"edu.agent.code/service/conversation"
	"edu.agent.code/service/cost"
	"edu.agent.code/service/dto"
	"edu.agent.code/service/quota"
	"edu.agent.code/service/rag"
	"edu.agent.code/utils/limiter"
	"edu.agent.code/utils/logger"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
	"net/http"
)

type Handler struct {
	adaptor        adaptor.IAdaptor
	cost           *cost.Service
	quota          *quota.Service
	session        *conversation.Service
	rag            *rag.Service
	projectIndexer *rag.ProjectIndexer
	auth           *auth.Service
	// profiles 供 GET/PUT /api/profile 直接读写画像（保存即入库，不必等下一轮对话）。
	profiles profile.IProfile
}

func NewHandler(ctx context.Context, adaptor adaptor.IAdaptor) (*Handler, error) {
	// 项目级语义索引：一个项目一个 Milvus collection，索引与检索都限定在本项目内。
	// 该实例在 conversation 层（索引 + 检索工具）和 api 层（状态查询）之间共享，避免出现两套状态。
	projectIndexer := rag.NewProjectIndexer(adaptor)
	// 项目级结构化索引（符号表 + 依赖图）：独立的 SQLite 文件，与 RAG 无关。
	// 它只是增强能力（更精确的符号/依赖查询），初始化失败只记日志、不阻断启动。
	codeIndexer, err := codeindex.New(adaptor.GetConfig().CodeIndex, adaptor)
	if err != nil {
		logger.Error("init code index failed, 结构化索引已关闭: %v", err)
		codeIndexer = nil
	}
	conversationSvc, err := conversation.NewService(ctx, adaptor, projectIndexer, codeIndexer)
	if err != nil {
		// 服务构造失败时把已经打开的索引库连接关掉，避免句柄泄漏。
		_ = codeIndexer.Close()
		return nil, err
	}
	ragSvc, err := rag.NewService(ctx, adaptor)
	if err != nil {
		return nil, err
	}
	return &Handler{
		adaptor:        adaptor,
		cost:           cost.NewService(adaptor),
		quota:          quota.NewService(adaptor),
		session:        conversationSvc,
		rag:            ragSvc,
		projectIndexer: projectIndexer,
		auth:           auth.NewService(adaptor),
		profiles:       profile.NewProfile(adaptor),
	}, nil
}

func (h *Handler) GetQuotaService() *quota.Service {
	return h.quota
}

func (h *Handler) GetCostService() *cost.Service {
	return h.cost
}

func (h *Handler) GetAuthService() *auth.Service {
	return h.auth
}

// GetConfig 暴露配置给路由层：限流豁免、IP 额度等策略都在配置里。
func (h *Handler) GetConfig() *config.Config {
	if h == nil || h.adaptor == nil {
		return &config.Config{}
	}
	return h.adaptor.GetConfig()
}

// GetRateLimiter 返回进程内共享的限流器（IP 级限流与用户级配额共用）。
func (h *Handler) GetRateLimiter() *limiter.Limiter {
	if h == nil || h.quota == nil {
		return nil
	}
	return h.quota.Limiter()
}

func (h *Handler) Close() error {
	if h == nil {
		return nil
	}
	if h.session != nil {
		err := h.session.Close()
		if err != nil {
			return err
		}
	}
	if h.rag != nil {
		err := h.rag.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// authUser 返回 Auth 中间件写入的当前登录用户。
// 未登录请求已被中间件以 401 拦截，这里不再回退到任何演示账号，
// 否则中间件覆盖不到的路由会静默获得一个高权限身份。
func (h *Handler) authUser(c *gin.Context) *common.UserInfo {
	value, _ := c.Get(common.CtxKeyAuthUser)
	user, _ := value.(*common.UserInfo)
	return user
}

func (h *Handler) traceIDFrom(ctx *gin.Context) string {
	spanCtx := trace.SpanContextFromContext(ctx.Request.Context())
	if spanCtx.HasTraceID() {
		return spanCtx.TraceID().String()
	}
	return common.GetUUIDHex()
}

func (h *Handler) writeResp(ctx *gin.Context, data any, errno common.Errno) {
	httpCode := errno.Code
	if httpCode == 0 {
		httpCode = http.StatusOK
	} else if httpCode >= http.StatusInternalServerError {
		httpCode = http.StatusInternalServerError
	}
	traceID := ctx.GetString(common.CtxKeyTraceID)
	ctx.JSON(httpCode, dto.Response{
		Code:    errno.Code,
		Message: errno.Error(),
		Data:    data,
		TraceID: traceID,
	})
	return
}
