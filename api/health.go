package api

import (
	"edu.agent.code/common"
	"edu.agent.code/service/dto"
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h *Handler) Health(c *gin.Context) {
	// TODO 这个健康应该处理两个地方
	// sqlite是否正常 milvus是否正常
	c.JSON(http.StatusOK, gin.H{"status": "OK"})
}

func (h *Handler) Version(c *gin.Context) {
	conf := h.adaptor.GetConfig()
	c.JSON(http.StatusOK, dto.Response[any]{
		Code:    common.OK.Code,
		Message: common.OK.Msg,
		Data: dto.Version{
			AppName:   conf.Server.AppName,
			Version:   conf.Server.Version,
			GoVersion: "1.26.5",
		},
		TraceID: c.GetString("trace_id"),
	})
}
