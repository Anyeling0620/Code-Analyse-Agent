//go:build e2e

// Package e2e 存放需要真实网络与真实外部服务（Milvus、embedding API）的端到端验证。
//
// 它用构建标签隔离，默认的 `go test ./...` 不会执行，必须显式指定：
//
//	$env:RAG_E2E=1; go test ./e2e/ -tags e2e -run TestRepoFetchAndProjectRAG -v -count=1
package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"edu.agent.code/adaptor"
	"edu.agent.code/config"
	"edu.agent.code/service/rag"
	"edu.agent.code/service/tool/repo_fetch"

	"github.com/cloudwego/eino/components/tool"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

// 体积很小、公开可访问的两个仓库，用于验证"多项目不混淆"与取样规则：
//   - repoA 含真实源码文件（.go），验证代码文件会被取样；
//   - repoB 只有一个**无扩展名的 README**，验证 filepath.Ext 为空的文件同样会被取样，
//     否则这类仓库会得到一个 ready 但 chunk=0 的空索引。
const (
	repoA = "https://github.com/golang/example.git"
	repoB = "https://github.com/octocat/Hello-World.git"
)

func TestMain(m *testing.M) {
	// go test 的工作目录是包目录；切回仓库根目录，config.InitConfig 才能读到 agent_code_local.yml。
	if err := os.Chdir(".."); err != nil {
		fmt.Fprintf(os.Stderr, "切换到仓库根目录失败: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func requireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("RAG_E2E") != "1" {
		t.Skip("端到端测试需要真实外部服务，请设置 RAG_E2E=1 后重跑")
	}
}

// fetchRepo 通过 repo_fetch 工具真实拉取仓库，并从 OnReady 钩子里取回结构化结果。
func fetchRepo(t *testing.T, source string, refresh bool) repo_fetch.Result {
	t.Helper()

	var (
		captured repo_fetch.Result
		called   bool
	)
	repoTool, err := repo_fetch.NewTool(repo_fetch.WithHooks(repo_fetch.Hooks{
		OnReady: func(_ context.Context, r repo_fetch.Result) {
			captured = r
			called = true
		},
	}))
	if err != nil {
		t.Fatalf("构造 repo_fetch 工具失败: %v", err)
	}
	invokable, ok := repoTool.(tool.InvokableTool)
	if !ok {
		t.Fatalf("repo_fetch 工具未实现 tool.InvokableTool")
	}
	args := fmt.Sprintf(`{"source":%q,"refresh":%t}`, source, refresh)
	out, err := invokable.InvokableRun(context.Background(), args)
	if err != nil {
		t.Fatalf("repo_fetch 执行出错: %v", err)
	}
	t.Logf("repo_fetch(%s) 返回:\n%s", source, out)
	if strings.Contains(out, "仓库准备失败") {
		t.Fatalf("repo_fetch 返回失败: %s", out)
	}
	if !called {
		t.Fatalf("OnReady 钩子未被调用，工具输出=%s", out)
	}
	if captured.Root == "" || captured.ProjectID == "" || captured.Commit == "" {
		t.Fatalf("repo_fetch 结果缺少关键字段: %+v", captured)
	}
	if _, err := os.Stat(captured.Root); err != nil {
		t.Fatalf("repo_fetch 返回的 root 不存在: %s (%v)", captured.Root, err)
	}
	return captured
}

func TestRepoFetchAndProjectRAG(t *testing.T) {
	requireE2E(t)

	conf := config.InitConfig()
	adpt, err := adaptor.NewAdaptor(conf)
	if err != nil {
		t.Fatalf("初始化 adaptor 失败: %v", err)
	}
	indexer := rag.NewProjectIndexer(adpt)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// ---- 1) 真实 clone：repoA ----
	a := fetchRepo(t, repoA, false)
	t.Logf("repoA: root=%s project_id=%s commit=%s size=%.2fMB files=%d",
		a.Root, a.ProjectID, a.Commit, a.SizeMB, a.FileCount)

	// ---- 2) 重复调用必须复用已有副本，且不重新 clone ----
	aAgain := fetchRepo(t, repoA, false)
	if !aAgain.Reused {
		t.Fatalf("同一仓库第二次调用应复用本地副本，实际 reused=false: %+v", aAgain)
	}
	if aAgain.Root != a.Root || aAgain.ProjectID != a.ProjectID {
		t.Fatalf("复用应落在同一目录与同一 project_id: %+v vs %+v", a, aAgain)
	}
	if aAgain.Commit != a.Commit {
		t.Fatalf("未刷新时 commit 不应变化: %s -> %s", a.Commit, aAgain.Commit)
	}

	// ---- 3) 真实建索引：chunk 数必须大于 0 ----
	// 这一步验证的是"代码仓库真的被取样并写进了向量库"。
	// 改造前 isDocFile 只认 6 种文档扩展名，这里必然得到 0 个 chunk。
	if err := indexer.EnsureIndexed(ctx, rag.ProjectRef{
		ProjectID: a.ProjectID,
		Root:      a.Root,
		Commit:    a.Commit,
	}); err != nil {
		t.Fatalf("repoA 建索引失败: %v", err)
	}
	statusA := indexer.Status(ctx, a.ProjectID)
	t.Logf("repoA 索引状态: %+v", statusA)
	if statusA.Status != rag.StatusReady {
		t.Fatalf("repoA 索引状态应为 ready，实际 %s（error=%s）", statusA.Status, statusA.LastError)
	}
	if statusA.ChunkCount == 0 {
		t.Fatalf("repoA 索引 chunk 数为 0：代码文件没有被取样，索引等于空库")
	}

	// ---- 4) 幂等：commit 未变时再次调用不应重复索引 ----
	if err := indexer.EnsureIndexed(ctx, rag.ProjectRef{
		ProjectID: a.ProjectID,
		Root:      a.Root,
		Commit:    a.Commit,
	}); err != nil {
		t.Fatalf("repoA 重复建索引失败: %v", err)
	}
	if again := indexer.Status(ctx, a.ProjectID); again.ChunkCount != statusA.ChunkCount {
		t.Fatalf("commit 未变时 chunk 数不应变化: %d -> %d", statusA.ChunkCount, again.ChunkCount)
	}

	// ---- 5) 真实检索：结果必须全部属于 repoA ----
	docsA := retrieveWithRetry(t, ctx, indexer, a.ProjectID, "readme example project")
	requireAllBelongTo(t, docsA, a)

	// ---- 6) 第二个项目：索引与检索都不能串味 ----
	b := fetchRepo(t, repoB, false)
	if b.ProjectID == a.ProjectID {
		t.Fatalf("两个不同仓库不应得到同一 project_id")
	}
	if b.Root == a.Root {
		t.Fatalf("两个不同仓库不应落在同一目录")
	}
	if err := indexer.EnsureIndexed(ctx, rag.ProjectRef{
		ProjectID: b.ProjectID,
		Root:      b.Root,
		Commit:    b.Commit,
	}); err != nil {
		t.Fatalf("repoB 建索引失败: %v", err)
	}
	statusB := indexer.Status(ctx, b.ProjectID)
	t.Logf("repoB 索引状态: %+v", statusB)
	if statusB.Status != rag.StatusReady || statusB.ChunkCount == 0 {
		t.Fatalf("repoB 索引不健康: %+v", statusB)
	}

	// repoB 只有一个无扩展名的 README，能检索到内容本身就证明它被取样入库了。
	docsB := retrieveWithRetry(t, ctx, indexer, b.ProjectID, "hello world")
	requireAllBelongTo(t, docsB, b)

	// 关键隔离断言：A 的检索结果里不允许出现 B 的任何片段，反之亦然。
	assertNoCrossProject(t, "repoA 检索结果混入 repoB", docsA, b.ProjectID)
	assertNoCrossProject(t, "repoB 检索结果混入 repoA", docsB, a.ProjectID)

	// ---- 7) 物理隔离：两个项目各自对应一个独立的 Milvus 集合 ----
	client := adpt.GetMilvusClient()
	if client == nil {
		t.Fatalf("Milvus client 为空")
	}
	base := conf.RAG.Milvus.Collection
	for _, projectID := range []string{a.ProjectID, b.ProjectID} {
		collection := base + "_" + projectID
		exists, err := client.HasCollection(ctx, milvusclient.NewHasCollectionOption(collection))
		if err != nil {
			t.Fatalf("查询集合 %s 是否存在失败: %v", collection, err)
		}
		if !exists {
			t.Fatalf("项目 %s 的独立集合 %s 不存在", projectID, collection)
		}
		t.Logf("项目 %s 使用独立集合: %s", projectID, collection)
	}
}

func retrieveWithRetry(t *testing.T, ctx context.Context, indexer *rag.ProjectIndexer, projectID, query string) []*docView {
	t.Helper()
	var last int
	for attempt := 1; attempt <= 5; attempt++ {
		docs, err := indexer.Retrieve(ctx, projectID, query, 10)
		if err != nil {
			t.Fatalf("检索失败 project=%s: %v", projectID, err)
		}
		last = len(docs)
		if last > 0 {
			views := make([]*docView, 0, len(docs))
			for _, doc := range docs {
				views = append(views, &docView{
					ID:          doc.ID,
					ProjectID:   fmt.Sprint(doc.MetaData[rag.ProjectIDMetadataKey]),
					ProjectRoot: fmt.Sprint(doc.MetaData[rag.ProjectRootMetadataKey]),
					SourcePath:  fmt.Sprint(doc.MetaData["source_path"]),
				})
			}
			t.Logf("project=%s query=%q 命中 %d 条，第一条 source=%s", projectID, query, len(views), views[0].SourcePath)
			return views
		}
		t.Logf("project=%s 第 %d 次检索为空，等待索引可见后重试", projectID, attempt)
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("project=%s 检索始终为空（最后返回 %d 条）", projectID, last)
	return nil
}

type docView struct {
	ID          string
	ProjectID   string
	ProjectRoot string
	SourcePath  string
}

func requireAllBelongTo(t *testing.T, docs []*docView, expect repo_fetch.Result) {
	t.Helper()
	if len(docs) == 0 {
		t.Fatalf("project=%s 未检索到任何片段", expect.ProjectID)
	}
	for _, doc := range docs {
		if doc.ProjectID != expect.ProjectID {
			t.Fatalf("检索结果掺杂了其它项目: 期望 %s 实际 %s (source=%s)", expect.ProjectID, doc.ProjectID, doc.SourcePath)
		}
		if doc.ProjectRoot != expect.Root {
			t.Fatalf("检索结果的 project_root 不匹配: 期望 %s 实际 %s", expect.Root, doc.ProjectRoot)
		}
	}
}

func assertNoCrossProject(t *testing.T, message string, docs []*docView, forbiddenProjectID string) {
	t.Helper()
	for _, doc := range docs {
		if doc.ProjectID == forbiddenProjectID {
			t.Fatalf("%s: 发现属于 %s 的片段 (source=%s)", message, forbiddenProjectID, doc.SourcePath)
		}
	}
}
