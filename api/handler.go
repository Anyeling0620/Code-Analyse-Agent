package api

import (
	"edu.agent.code/adaptor"
	"edu.agent.code/common"
	"edu.agent.code/service/cost"
	"edu.agent.code/service/dto"
	"edu.agent.code/service/quota"
	"github.com/gin-gonic/gin"
	"net/http"
)

type Handler struct {
	adaptor adaptor.IAdaptor
	cost    *cost.Service
	quota   *quota.Service
}

func NewHandler(adaptor adaptor.IAdaptor) *Handler {
	return &Handler{
		adaptor: adaptor,
		cost:    cost.NewService(adaptor),
		quota:   quota.NewService(adaptor),
	}
}

func (h *Handler) GetQuotaService() *quota.Service {
	return h.quota
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
