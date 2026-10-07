package api

import (
	"edu.agent.code/adaptor"
	"edu.agent.code/common"
	"edu.agent.code/service/dto"
	"github.com/gin-gonic/gin"
)

func (h *Handler) Health(c *gin.Context) {
	// 只报告依赖状态，不因依赖缺位返回非 200：
	// /healthz 同时是拉取式部署的探活目标，一旦 Milvus 缺位就返回 5xx，
	// 正常的部署会被判成健康检查失败并回滚。
	h.writeResp(c, map[string]any{
		"status":       "OK",
		"dependencies": healthDependencies(h.adaptor),
	}, common.OK)
}

// healthDependencies 汇总外部依赖的连通状态，便于排障。
// Milvus 缺位过去是静默的——服务照旧返回 OK，只有翻 goroutine dump 才能发现。
func healthDependencies(a adaptor.IAdaptor) map[string]any {
	deps := make(map[string]any, 2)
	if a == nil {
		return deps
	}
	deps["sqlite"] = "ok"
	if db := a.GetDB(); db == nil {
		deps["sqlite"] = "down"
	} else if sqlDB, err := db.DB(); err != nil {
		deps["sqlite"] = "down"
	} else if err := sqlDB.Ping(); err != nil {
		deps["sqlite"] = "down"
	}
	// 向量库走可选接口：adaptor 未实现时就不报告，不为健康检查扩大 IAdaptor。
	if reporter, ok := a.(milvusStatusReporter); ok {
		if ready, err := reporter.MilvusStatus(); ready {
			deps["milvus"] = "ok"
		} else {
			deps["milvus"] = "down"
			if err != nil {
				deps["milvus_error"] = err.Error()
			}
		}
	}
	return deps
}

// milvusStatusReporter 是 adaptor 可选实现的接口：把向量库连通状态报给健康检查。
type milvusStatusReporter interface {
	MilvusStatus() (bool, error)
}

func (h *Handler) Version(c *gin.Context) {
	conf := h.adaptor.GetConfig()
	resp := dto.Version{
		AppName:    conf.Server.AppName,
		Version:    conf.Server.Version,
		GoVersion:  "1.26.5",
		GuestLogin: conf.Auth.Guest.Enabled,
	}
	h.writeResp(c, resp, common.OK)
}
