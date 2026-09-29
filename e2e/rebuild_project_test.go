//go:build e2e

package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	"edu.agent.code/adaptor"
	"edu.agent.code/config"
	"edu.agent.code/service/rag"
)

// TestRebuildProjectIndex 用指定的本地仓库根目录重建某个项目级索引。
//
// 用途：切分策略、取样规则、脱敏逻辑改动后，需要让已有 collection 重跑一遍
// 才算生效（索引是写入时行为）。本用例只负责触发重建并确认状态，不做断言式校验。
//
//	$env:RAG_E2E=1
//	$env:REBUILD_ROOT="<本地仓库路径>"
//	$env:REBUILD_PID="<project_id>"
//	$env:REBUILD_COMMIT="<commit sha，可留空>"
//	go test ./e2e -tags e2e -run TestRebuildProjectIndex -v -count=1
func TestRebuildProjectIndex(t *testing.T) {
	if os.Getenv("RAG_E2E") != "1" {
		t.Skip("需要真实 Milvus 与 embedding 服务，设置 RAG_E2E=1 后重跑")
	}

	root := os.Getenv("REBUILD_ROOT")
	projectID := os.Getenv("REBUILD_PID")
	commit := os.Getenv("REBUILD_COMMIT")
	if root == "" || projectID == "" {
		t.Fatal("需要设置 REBUILD_ROOT 与 REBUILD_PID")
	}

	conf := config.InitConfig()
	adpt, err := adaptor.NewAdaptor(conf)
	if err != nil {
		t.Fatalf("初始化 adaptor 失败: %v", err)
	}
	indexer := rag.NewProjectIndexer(adpt)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()

	start := time.Now()
	if err := indexer.EnsureIndexed(ctx, rag.ProjectRef{
		ProjectID: projectID,
		Root:      root,
		Commit:    commit,
	}); err != nil {
		t.Fatalf("重建索引失败: %v", err)
	}
	status := indexer.Status(ctx, projectID)
	t.Logf("重建完成，用时 %s，状态 %+v", time.Since(start).Round(time.Millisecond), status)
	if status.Status != rag.StatusReady {
		t.Fatalf("重建后状态应为 ready，实际 %s（error=%s）", status.Status, status.LastError)
	}
}
