package api

import (
	"edu.agent.code/common"
	"edu.agent.code/service/dto"
	"github.com/gin-gonic/gin"
)

func (h *Handler) Retriever(c *gin.Context) {
	req := &dto.RetrieverReq{}
	if err := c.ShouldBindJSON(req); err != nil {
		h.writeResp(c, nil, common.ParamError.WithError(err))
		return
	}
	resp, errno := h.rag.Retriever(c.Request.Context(), req)
	h.writeResp(c, resp, errno)
}
