package rag

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"edu.agent.code/adaptor"
	"edu.agent.code/common"
	"edu.agent.code/config"
	"edu.agent.code/service/do"
	"edu.agent.code/service/projectid"

	"github.com/cloudwego/eino/schema"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// fakeAdaptor 只实现项目索引器用到的那部分 adaptor 能力，让隔离相关的逻辑可以离线验证。
// Milvus client、DB 与 Redis 都返回 nil：调用方一旦真的去连外部依赖就会立刻暴露，测试不会静默走网络。
type fakeAdaptor struct {
	conf *config.Config
	db   *gorm.DB
}

func (f *fakeAdaptor) GetConfig() *config.Config { return f.conf }
func (f *fakeAdaptor) GetDB() *gorm.DB           { return f.db }
func (f *fakeAdaptor) GetMilvusClient() *milvusclient.Client {
	return nil
}
func (f *fakeAdaptor) GetRedis() *redis.Client { return nil }

var _ adaptor.IAdaptor = (*fakeAdaptor)(nil)

func newTestIndexer(t *testing.T, enabled bool) *ProjectIndexer {
	t.Helper()
	conf := &config.Config{RAG: config.RAG{
		Enabled:      enabled,
		ChunkSize:    1200,
		ChunkOverlap: 200,
		MaxFileBytes: 4096,
		Milvus:       config.Milvus{Collection: "test_docs"},
	}}
	return NewProjectIndexer(&fakeAdaptor{conf: conf})
}

// fakeSessionLookup 替代会话仓储，供 resolveCurrent 的 DB 分支测试使用。
type fakeSessionLookup struct {
	session *do.SessionContext
	err     error
}

func (f *fakeSessionLookup) GetByID(ctx context.Context, sessionID string) (*do.SessionContext, error) {
	return f.session, f.err
}

func TestProjectCollectionNameKeepsProjectsApart(t *testing.T) {
	first := projectCollectionName("edu_agent_code_docs", "p0000000000000001")
	second := projectCollectionName("edu_agent_code_docs", "p0000000000000002")
	if first == second {
		t.Fatalf("不同项目必须落在不同集合：%s == %s", first, second)
	}
	if first != "edu_agent_code_docs_p0000000000000001" {
		t.Fatalf("集合名不符合预期：%s", first)
	}
	// 默认基础名 edu.agent.code 里有非法字符，必须被清洗，否则建集合会失败。
	dotted := projectCollectionName("edu.agent.code", "pab")
	if strings.Contains(dotted, ".") {
		t.Fatalf("集合名不应保留点号：%s", dotted)
	}
	if dotted != "edu_agent_code_pab" {
		t.Fatalf("清洗结果不符合预期：%s", dotted)
	}
}

func TestProjectFilterExprUsesJSONPath(t *testing.T) {
	expr := projectFilterExpr("p1234567890abcdef")
	want := `metadata["project_id"] == "p1234567890abcdef"`
	if expr != want {
		t.Fatalf("过滤表达式不符合预期：%s", expr)
	}
}

func TestFilterByProjectDropsForeignChunks(t *testing.T) {
	docs := []*schema.Document{
		{ID: "a", Content: "own", MetaData: map[string]any{ProjectIDMetadataKey: "pA"}},
		{ID: "b", Content: "foreign", MetaData: map[string]any{ProjectIDMetadataKey: "pB"}},
		{ID: "c", Content: "no metadata", MetaData: map[string]any{}},
		nil,
	}
	filtered := filterByProject(docs, "pA")
	if len(filtered) != 1 || filtered[0].ID != "a" {
		t.Fatalf("检索结果必须只保留本项目 chunk，实际：%+v", filtered)
	}
}

func TestChunkIDIsolatedByProject(t *testing.T) {
	first := chunkID("pA", "/repo/main.go", 0, "package main")
	second := chunkID("pB", "/repo/main.go", 0, "package main")
	if first == second {
		t.Fatalf("不同项目的同名同内容文件不能共用 chunk ID：%s", first)
	}
	// 同一项目同一输入必须稳定，否则重复索引会产生新 ID、留下旧块。
	if again := chunkID("pA", "/repo/main.go", 0, "package main"); again != first {
		t.Fatalf("chunk ID 必须稳定：%s != %s", again, first)
	}
	// 全局文档索引（无项目）保持既有的非空行为。
	if global := chunkID("", "/docs/a.md", 1, "x"); global == "" {
		t.Fatal("全局索引的 chunk ID 不能为空")
	}
}

func TestIsDocFileCoversCodeAndBuildFiles(t *testing.T) {
	indexable := []string{
		"main.go", "router/router.go", "web/src/App.tsx", "service/a.py", "api/v1/routes.java",
		"internal/handler.rs", "pom.xml", "go.mod", "Dockerfile", "Makefile",
		"docker-compose.yml", "README.md", ".env.example", "sql/schema.sql",
		// 无扩展名的仓库标配文件：真实仓库（如 octocat/Hello-World）只有一个 README，
		// 漏掉它会让该仓库索引到 0 个 chunk。
		"README", "CHANGELOG", "CONTRIBUTING",
	}
	for _, path := range indexable {
		if !isDocFile(path) {
			t.Errorf("%s 应该被索引", path)
		}
	}
	// 扩展名/文件名的取舍由 isDocFile 决定；node_modules、dist 这类目录由 shouldSkipDir 在遍历时整目录跳过。
	skipped := []string{"app.bin", "go.sum", "LICENSE"}
	for _, path := range skipped {
		if isDocFile(path) {
			t.Errorf("%s 不应该被索引", path)
		}
	}
	for _, dir := range []string{"node_modules", "dist", "vendor", ".git", "build"} {
		if !shouldSkipDir(dir) {
			t.Errorf("%s 目录应当被整目录跳过", dir)
		}
	}
	if shouldSkipDir("service") {
		t.Error("业务代码目录不应被跳过")
	}
}

func TestIsSensitiveFileSkipsOnlySecrets(t *testing.T) {
	for _, path := range []string{".env", "deploy/.env.production", "key.pem", "cert.key", ".ssh/id_rsa"} {
		if !isSensitiveFile(path) {
			t.Errorf("%s 是敏感文件，必须跳过", path)
		}
	}
	// 模板文件是配置说明，需要入库。
	if isSensitiveFile(".env.example") {
		t.Error(".env.example 应当被允许索引")
	}
	if isSensitiveFile("config/app.yaml") {
		t.Error("普通配置文件不应被判定为敏感文件")
	}
}

func TestLoadDocsInScopeAddsProjectMetadata(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("go.mod", "module demo\n")
	write("main.go", "package main\n\nfunc main() {}\n")
	write("router/router.go", "package router\n")
	write("README.md", "# demo\n")
	write(".env.example", "API_KEY=\n")
	write(".env", "API_KEY=real-secret\n")
	write("id_rsa", "PRIVATE KEY\n")
	write("node_modules/pkg/index.js", "module.exports = 1\n")
	write("dist/bundle.js", "console.log(1)\n")
	write("big.go", strings.Repeat("x", 8192))

	conf := config.RAG{ChunkSize: 1200, ChunkOverlap: 200, MaxFileBytes: 4096}
	docs, err := loadDocsInScope(context.Background(), root, conf, docScope{
		ProjectID:   "pabcdef",
		ProjectRoot: root,
		Commit:      "deadbeef",
	})
	if err != nil {
		t.Fatalf("loadDocsInScope: %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("样例仓库应当切出 chunk，实际为空（说明取样规则把代码文件全过滤掉了）")
	}

	sources := make(map[string]bool)
	for _, doc := range docs {
		source := doc.MetaData["source_path"].(string)
		sources[source] = true
		if got := doc.MetaData[ProjectIDMetadataKey]; got != "pabcdef" {
			t.Errorf("%s 的 project_id 元数据缺失：%v", source, got)
		}
		if got := doc.MetaData[ProjectRootMetadataKey]; got != root {
			t.Errorf("%s 的 project_root 元数据缺失：%v", source, got)
		}
		if got := doc.MetaData[CommitMetadataKey]; got != "deadbeef" {
			t.Errorf("%s 的 commit_sha 元数据缺失：%v", source, got)
		}
		if doc.ID == "" {
			t.Errorf("%s 的 chunk ID 不能为空", source)
		}
	}

	for _, expected := range []string{"go.mod", "main.go", "router/router.go", "README.md", ".env.example"} {
		if !sources[expected] {
			t.Errorf("预期索引 %s，实际 %v", expected, sources)
		}
	}
	for _, unexpected := range []string{".env", "id_rsa", "node_modules/pkg/index.js", "dist/bundle.js", "big.go"} {
		if sources[unexpected] {
			t.Errorf("不应索引 %s，实际 %v", unexpected, sources)
		}
	}
}

func TestEnsureIndexedShortCircuitsOnSameCommit(t *testing.T) {
	indexer := newTestIndexer(t, true)
	indexer.setStatus(Status{ProjectID: "pReady", Status: StatusReady, Commit: "c1", ChunkCount: 3})

	// root 故意指向不存在的目录：一旦没有短路，就会走到取样并返回错误。
	if err := indexer.EnsureIndexed(context.Background(), ProjectRef{
		ProjectID: "pReady",
		Root:      filepath.Join(t.TempDir(), "not-exist"),
		Commit:    "c1",
	}); err != nil {
		t.Fatalf("commit 未变时必须幂等短路，实际返回错误：%v", err)
	}
	if status := indexer.Status(context.Background(), "pReady"); status.Status != StatusReady || status.Commit != "c1" {
		t.Fatalf("短路不应改变已有状态：%+v", status)
	}
}

func TestEnsureIndexedSkipsWhenAlreadyRunning(t *testing.T) {
	indexer := newTestIndexer(t, true)
	indexer.running["pBusy"] = true

	// 同项目已有索引在跑：直接返回，不再做任何取样。
	if err := indexer.EnsureIndexed(context.Background(), ProjectRef{
		ProjectID: "pBusy",
		Root:      filepath.Join(t.TempDir(), "not-exist"),
	}); err != nil {
		t.Fatalf("单飞期间重复触发应直接返回，实际：%v", err)
	}
	if status := indexer.Status(context.Background(), "pBusy"); status.Status != StatusNone {
		t.Fatalf("被单飞挡下的调用不应写状态：%+v", status)
	}
	indexer.mu.Lock()
	running := indexer.running["pBusy"]
	indexer.mu.Unlock()
	if !running {
		t.Fatal("被单飞挡下的调用不应清掉在途标记")
	}
}

func TestEnsureIndexedMarksFailedOnBadRoot(t *testing.T) {
	indexer := newTestIndexer(t, true)
	err := indexer.EnsureIndexed(context.Background(), ProjectRef{
		ProjectID: "pBad",
		Root:      filepath.Join(t.TempDir(), "missing"),
		Commit:    "c1",
	})
	if err == nil {
		t.Fatal("根目录不存在时应当返回错误")
	}
	status := indexer.Status(context.Background(), "pBad")
	if status.Status != StatusFailed {
		t.Fatalf("失败状态未记录：%+v", status)
	}
	if status.LastError == "" {
		t.Fatal("失败原因应当写入 LastError")
	}
	if indexer.running["pBad"] {
		t.Fatal("失败后必须释放单飞标记，否则该项目再也不会重试")
	}
}

func TestEnsureIndexedIsNoopWhenRAGDisabled(t *testing.T) {
	indexer := newTestIndexer(t, false)
	if err := indexer.EnsureIndexed(context.Background(), ProjectRef{
		ProjectID: "pOff",
		Root:      filepath.Join(t.TempDir(), "missing"),
	}); err != nil {
		t.Fatalf("RAG 关闭时索引器应是空实现，实际：%v", err)
	}
	tool, err := indexer.Tool(context.Background())
	if err != nil || tool != nil {
		t.Fatalf("RAG 关闭时不应挂载检索工具：tool=%v err=%v", tool, err)
	}
}

func TestStatusUnknownProjectIsNone(t *testing.T) {
	indexer := newTestIndexer(t, true)
	status := indexer.Status(context.Background(), "pUnknown")
	if status.Status != StatusNone || status.ProjectID != "pUnknown" {
		t.Fatalf("未登记项目应返回 none：%+v", status)
	}
	if empty := indexer.Status(context.Background(), "  "); empty.Status != StatusNone {
		t.Fatalf("空 project_id 应返回 none：%+v", empty)
	}
}

func TestGitOriginRemoteParsing(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	configBody := "[core]\n\tbare = false\n[remote \"upstream\"]\n\turl = https://example.com/upstream.git\n" +
		"[branch \"main\"]\n\tremote = origin\n[remote \"origin\"]\n\turl = git@github.com:owner/repo.git\n"
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(configBody), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if got := gitOriginRemote(root); got != "git@github.com:owner/repo.git" {
		t.Fatalf("origin 解析错误：%q", got)
	}
	// 非 git 目录退化为按本地路径派生。
	if got := gitOriginRemote(t.TempDir()); got != "" {
		t.Fatalf("非 git 目录不应解析出 remote：%q", got)
	}
}

func TestRefFromRootPrefersRemoteLikeRepoFetch(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	configBody := "[remote \"origin\"]\n\turl = https://github.com/owner/repo.git\n"
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(configBody), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ref := refFromRootWith(root, nil)
	if want := projectid.Derive(root, "https://github.com/owner/repo.git"); ref.ProjectID != want {
		t.Fatalf("必须与 repo_fetch 用同一个 remote 派生：got=%s want=%s", ref.ProjectID, want)
	}
	// 不同 clone 目录、同一个 remote 必须得到同一个 project_id，否则检索会找不到已建好的集合。
	other := t.TempDir()
	otherGit := filepath.Join(other, ".git")
	if err := os.MkdirAll(otherGit, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(otherGit, "config"), []byte(configBody), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if otherRef := refFromRootWith(other, nil); otherRef.ProjectID != ref.ProjectID {
		t.Fatalf("同一 remote 的两个 clone 必须同 ID：%s != %s", otherRef.ProjectID, ref.ProjectID)
	}
}

func TestResolveCurrentPrefersRecordedSessionProject(t *testing.T) {
	indexer := newTestIndexer(t, true)
	ctx := common.WithUserAndSession(context.Background(), "u1", "s1")

	// 建索引时记下的 session→项目优先级最高：本轮 repo_fetch 刚拿到项目就能被解析。
	indexer.remember(ctx, ProjectRef{ProjectID: "pRecorded", Root: "/repo/a"})
	ref, ok := indexer.resolveCurrent(ctx)
	if !ok || ref.ProjectID != "pRecorded" {
		t.Fatalf("应优先命中已记录的项目：%+v ok=%v", ref, ok)
	}
}

func TestResolveCurrentFallsBackToSessionRoot(t *testing.T) {
	indexer := newTestIndexer(t, true)
	root := t.TempDir()
	indexer.sessions = &fakeSessionLookup{session: &do.SessionContext{CurrentProjectRoot: root}}
	ctx := common.WithUserAndSession(context.Background(), "u1", "s2")

	ref, ok := indexer.resolveCurrent(ctx)
	if !ok {
		t.Fatal("会话里已有项目根目录时应当能解析出项目")
	}
	if ref.Root != root {
		t.Fatalf("项目根目录不符：%s != %s", ref.Root, root)
	}
	if want := projectid.Derive(root, ""); ref.ProjectID != want {
		t.Fatalf("非 git 目录应按本地路径派生：got=%s want=%s", ref.ProjectID, want)
	}
	// 没有会话 ID 时不能凭空猜项目。
	if _, ok := indexer.resolveCurrent(context.Background()); ok {
		t.Fatal("没有会话上下文时不应猜出项目")
	}
}
