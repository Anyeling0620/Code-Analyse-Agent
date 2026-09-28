package api

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/common"
	"edu.agent.code/service/conversation"
	"edu.agent.code/service/cost"
	"edu.agent.code/service/dto"
	"edu.agent.code/service/quota"
	"edu.agent.code/service/rag"
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
}

func NewHandler(ctx context.Context, adaptor adaptor.IAdaptor) (*Handler, error) {
	// 项目级语义索引：一个项目一个 Milvus collection，索引与检索都限定在本项目内。
	// 该实例在 conversation 层（索引 + 检索工具）和 api 层（状态查询）之间共享，避免出现两套状态。
	projectIndexer := rag.NewProjectIndexer(adaptor)
	conversationSvc, err := conversation.NewService(ctx, adaptor, projectIndexer)
	if err != nil {
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
	}, nil
}

func (h *Handler) GetQuotaService() *quota.Service {
	return h.quota
}

func (h *Handler) GetCostService() *cost.Service {
	return h.cost
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

func (h *Handler) authUser(c *gin.Context) *common.UserInfo {
	value, _ := c.Get(common.CtxKeyAuthUser)
	user, _ := value.(*common.UserInfo)
	if user == nil {
		return &common.UserInfo{
			UserID: "demo-user", Plan: common.PlanPro,
		}
	}
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
