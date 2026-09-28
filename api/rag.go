package api

import (
	"edu.agent.code/common"
	"edu.agent.code/service/dto"
	"edu.agent.code/service/projectid"
	"github.com/gin-gonic/gin"
	"strings"
	"time"
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

// ProjectIndexStatus 查询某个项目代码语义索引的构建状态。
// 用于排查"仓库已拉取但检索不到内容"这类问题，也供端到端验证使用。
func (h *Handler) ProjectIndexStatus(c *gin.Context) {
	req := &dto.ProjectIndexStatusReq{}
	if err := c.ShouldBindQuery(req); err != nil {
		h.writeResp(c, nil, common.ParamError.WithError(err))
		return
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		root := strings.TrimSpace(req.Root)
		if root == "" {
			h.writeResp(c, nil, common.ParamError.WithMsg("project_id 与 root 至少提供一个"))
			return
		}
		projectID = projectid.Derive(root, "")
	}
	if h.projectIndexer == nil {
		h.writeResp(c, nil, common.ServerError.WithMsg("项目索引未启用"))
		return
	}
	status := h.projectIndexer.Status(c.Request.Context(), projectID)
	resp := &dto.ProjectIndexStatusDto{
		ProjectID:  status.ProjectID,
		Status:     string(status.Status),
		Commit:     status.Commit,
		ChunkCount: status.ChunkCount,
		LastError:  status.LastError,
	}
	if !status.IndexedAt.IsZero() {
		resp.IndexedAt = status.IndexedAt.Format(time.DateTime)
	}
	h.writeResp(c, resp, common.OK)
}
