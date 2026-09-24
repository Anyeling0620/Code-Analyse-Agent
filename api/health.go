package api

import (
	"edu.agent.code/common"
	"edu.agent.code/service/dto"
	"github.com/gin-gonic/gin"
)

func (h *Handler) Health(c *gin.Context) {
	// TODO 这个健康应该处理两个地方
	// sqlite是否正常 milvus是否正常
	h.writeResp(c, map[string]any{
		"status": "OK",
	}, common.OK)
}

func (h *Handler) Version(c *gin.Context) {
	conf := h.adaptor.GetConfig()
	resp := dto.Version{
		AppName:   conf.Server.AppName,
		Version:   conf.Server.Version,
		GoVersion: "1.26.5",
	}
	h.writeResp(c, resp, common.OK)
}
