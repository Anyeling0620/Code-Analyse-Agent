package codeindex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"edu.agent.code/common"
	"edu.agent.code/config"
)

const testSession = "session-code-index"
const testProject = "p-test-project"

func testContext() context.Context {
	return common.WithUserAndSession(context.Background(), "u-test", testSession)
}

// newTestIndexer 打开一个临时目录里的独立索引库，不碰任何真实数据。
func newTestIndexer(t *testing.T) *Indexer {
	t.Helper()
	path := filepath.Join(t.TempDir(), "code_index.db")
	indexer, err := New(config.CodeIndex{Enable: true, Path: path}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if indexer == nil {
		t.Fatal("New 返回 nil，期望启用状态下的索引器")
	}
	t.Cleanup(func() {
		_ = indexer.Close()
	})
	return indexer
}

// markReady 直接把项目登记成"已就绪"，让查询侧测试不依赖抽取器。
func markReady(ix *Indexer, root string) {
	ix.remember(testContext(), Ref{ProjectID: testProject, Root: root})
	ix.setStatus(Status{ProjectID: testProject, Status: StatusReady, FileCount: 1})
}

func sampleFiles() []fileRow {
	return []fileRow{
		{ProjectID: testProject, Path: "service/rag/project.go", Language: "go", Size: 100, LineCount: 10, SymbolCount: 2},
		{ProjectID: testProject, Path: "service/rag/tool.go", Language: "go", Size: 80, LineCount: 8, SymbolCount: 1},
		{ProjectID: testProject, Path: "main.go", Language: "go", Size: 20, LineCount: 2, SymbolCount: 1},
	}
}

func sampleSymbols() []symbolRow {
	return []symbolRow{
		{ProjectID: testProject, Path: "service/rag/project.go", Name: "ProjectIndexer", Kind: KindStruct, Signature: "type ProjectIndexer struct", LineStart: 12, LineEnd: 30},
		{ProjectID: testProject, Path: "service/rag/project.go", Name: "EnsureIndexed", Kind: KindMethod, Parent: "ProjectIndexer", Signature: "func (p *ProjectIndexer) EnsureIndexed(ctx context.Context, ref ProjectRef) error", LineStart: 40, LineEnd: 80},
		{ProjectID: testProject, Path: "service/rag/tool.go", Name: "EnsureIndexedAsync", Kind: KindFunc, Signature: "func EnsureIndexedAsync()", LineStart: 5, LineEnd: 9},
		{ProjectID: testProject, Path: "main.go", Name: "main", Kind: KindFunc, Signature: "func main()", LineStart: 3, LineEnd: 5},
	}
}

func sampleEdges() []edgeRow {
	return []edgeRow{
		{ProjectID: testProject, FromPath: "main.go", ToRaw: "edu.agent.code/service/rag", ToPath: "service/rag/", Kind: EdgeImport, Confidence: ConfidenceExact, Line: 5},
		{ProjectID: testProject, FromPath: "service/rag/project.go", FromSymbol: "ProjectIndexer", ToRaw: "BaseIndexer", Kind: EdgeExtends, Confidence: ConfidenceHeuristic, Line: 12},
	}
}

func TestPathIndexResolveGoModule(t *testing.T) {
	index := newPathIndex(sampleFiles(), "")
	index.module = "edu.agent.code"

	if got := index.resolve("main.go", "edu.agent.code/service/rag"); got != "service/rag/" {
		t.Fatalf("模块内包导入应解析成目录，得到 %q", got)
	}
	if got := index.resolve("main.go", "fmt"); got != "" {
		t.Fatalf("标准库不应解析出本地路径，得到 %q", got)
	}
	if got := index.resolve("main.go", "./service/rag"); got != "service/rag/" {
		t.Fatalf("相对目录导入应解析成目录，得到 %q", got)
	}
}

func TestPathIndexResolveRelativeAndPython(t *testing.T) {
	files := []fileRow{
		{ProjectID: testProject, Path: "web/src/index.ts"},
		{ProjectID: testProject, Path: "web/src/lib/index.ts"},
		{ProjectID: testProject, Path: "app/pkg/util.py"},
		{ProjectID: testProject, Path: "app/pkg/__init__.py"},
	}
	index := newPathIndex(files, "")

	if got := index.resolve("web/src/index.ts", "./lib"); got != "web/src/lib/index.ts" {
		t.Fatalf("应解析到 lib/index.ts，得到 %q", got)
	}
	if got := index.resolve("app/pkg/util.py", "pkg.util"); got != "app/pkg/util.py" {
		t.Fatalf("Python 绝对导入应解析到 app/pkg/util.py，得到 %q", got)
	}
	if got := index.resolve("app/pkg/util.py", "."); got != "app/pkg/__init__.py" {
		t.Fatalf("Python 相对导入未命中文件时应回落到包目录，得到 %q", got)
	}
}

func TestStoreRoundTripAndQueries(t *testing.T) {
	ix := newTestIndexer(t)
	ctx := context.Background()
	root := t.TempDir()

	if err := ix.store.replaceProject(ctx, projectRow{
		ProjectID: testProject,
		Root:      root,
		CommitSHA: "abc123",
		IndexedAt: 1700000000,
	}, sampleFiles(), sampleSymbols(), sampleEdges()); err != nil {
		t.Fatalf("replaceProject: %v", err)
	}

	project, found, err := ix.store.project(ctx, testProject)
	if err != nil || !found {
		t.Fatalf("读项目行失败 found=%v err=%v", found, err)
	}
	if project.FileCount != 3 || project.SymbolCount != 4 || project.EdgeCount != 2 {
		t.Fatalf("统计写入不正确: %+v", project)
	}

	// 等值优先：EnsureIndexed 应排在 EnsureIndexedAsync 之前。
	rows, err := ix.store.findSymbols(ctx, testProject, "EnsureIndexed", "", 10)
	if err != nil {
		t.Fatalf("findSymbols: %v", err)
	}
	if len(rows) < 2 || rows[0].Name != "EnsureIndexed" {
		t.Fatalf("等值匹配应排第一，得到 %+v", rows)
	}

	// 目录形式的 to_path 要能匹配到目录下的文件。
	incoming, err := ix.store.importsInto(ctx, testProject, "service/rag/tool.go")
	if err != nil {
		t.Fatalf("importsInto: %v", err)
	}
	if len(incoming) != 1 || incoming[0].FromPath != "main.go" {
		t.Fatalf("目录前缀匹配失败: %+v", incoming)
	}

	// 重复写入必须覆盖旧行，而不是累加。
	if err := ix.store.replaceProject(ctx, project, sampleFiles()[:1], sampleSymbols()[:1], nil); err != nil {
		t.Fatalf("二次 replaceProject: %v", err)
	}
	files, err := ix.store.filesUnder(ctx, testProject, "", 0)
	if err != nil {
		t.Fatalf("filesUnder: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("重建应清掉旧行，剩余 %d 个文件", len(files))
	}
}

func TestToolsReturnReadyResults(t *testing.T) {
	ix := newTestIndexer(t)
	ctx := testContext()
	root := t.TempDir()
	markReady(ix, root)

	if err := ix.store.replaceProject(context.Background(), projectRow{
		ProjectID: testProject,
		Root:      root,
		IndexedAt: 1700000000,
	}, sampleFiles(), sampleSymbols(), sampleEdges()); err != nil {
		t.Fatalf("replaceProject: %v", err)
	}

	outline, err := ix.outlineTool(ctx, outlineInput{Path: "service/rag/project.go"})
	if err != nil {
		t.Fatalf("outline: %v", err)
	}
	if !strings.Contains(outline, "EnsureIndexed") || !strings.Contains(outline, "L40") {
		t.Fatalf("文件 outline 缺少符号或行号:\n%s", outline)
	}

	dirOutline, err := ix.outlineTool(ctx, outlineInput{Path: "service/rag"})
	if err != nil {
		t.Fatalf("dir outline: %v", err)
	}
	if !strings.Contains(dirOutline, "service/rag/tool.go") {
		t.Fatalf("目录 outline 未列出文件:\n%s", dirOutline)
	}

	found, err := ix.findSymbolTool(ctx, findSymbolInput{Name: "EnsureIndexed", Kind: KindMethod})
	if err != nil {
		t.Fatalf("find symbol: %v", err)
	}
	if !strings.Contains(found, "service/rag/project.go:40") {
		t.Fatalf("find symbol 未返回 path:line:\n%s", found)
	}

	deps, err := ix.fileDepsTool(ctx, fileDepsInput{Path: "main.go"})
	if err != nil {
		t.Fatalf("file deps: %v", err)
	}
	if !strings.Contains(deps, "service/rag/") {
		t.Fatalf("依赖输出缺少解析结果或未解析占位:\n%s", deps)
	}

	inbound, err := ix.fileDepsTool(ctx, fileDepsInput{Path: "service/rag/tool.go"})
	if err != nil {
		t.Fatalf("file deps inbound: %v", err)
	}
	if !strings.Contains(inbound, "main.go") {
		t.Fatalf("入边未包含 main.go:\n%s", inbound)
	}

	relations, err := ix.typeRelationsTool(ctx, typeRelationsInput{Name: "ProjectIndexer"})
	if err != nil {
		t.Fatalf("type relations: %v", err)
	}
	if !strings.Contains(relations, "BaseIndexer") || !strings.Contains(relations, "启发式") {
		t.Fatalf("类型关系未标注启发式:\n%s", relations)
	}
}

func TestToolsWithoutProjectReturnTips(t *testing.T) {
	ix := newTestIndexer(t)
	out, err := ix.outlineTool(context.Background(), outlineInput{Path: "main.go"})
	if err != nil {
		t.Fatalf("outline: %v", err)
	}
	if !strings.Contains(out, "还没有确定项目") {
		t.Fatalf("无项目时应返回可操作提示，得到 %q", out)
	}
}

func TestEnsureIndexedBuildsRealTree(t *testing.T) {
	ix := newTestIndexer(t)
	ctx := testContext()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/tiny\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	source := "package pkg\n\nimport \"example.com/tiny/lib\"\n\ntype Store struct {\n\n field int\n}\n"
	if err := os.WriteFile(filepath.Join(root, "pkg", "store.go"), []byte(source), 0o644); err != nil {
		t.Fatalf("write store.go: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0o755); err != nil {
		t.Fatalf("mkdir lib: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "lib", "lib.go"), []byte("package lib\n"), 0o644); err != nil {
		t.Fatalf("write lib.go: %v", err)
	}
	// 被跳过的目录不该进入索引。
	skipped := filepath.Join(root, "node_modules", "dep")
	if err := os.MkdirAll(skipped, 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skipped, "dep.go"), []byte("package dep\n"), 0o644); err != nil {
		t.Fatalf("write dep.go: %v", err)
	}

	ref := Ref{ProjectID: testProject, Root: root, Commit: "deadbeef"}
	if err := ix.EnsureIndexed(ctx, ref); err != nil {
		t.Fatalf("EnsureIndexed: %v", err)
	}
	status := ix.Status(ctx, testProject)
	if status.Status != StatusReady {
		t.Fatalf("索引后状态应为 ready，得到 %+v", status)
	}
	if status.FileCount != 2 {
		t.Fatalf("应只索引 2 个文件（node_modules 被跳过），得到 %d", status.FileCount)
	}
	files, err := ix.store.filesUnder(context.Background(), testProject, "", 0)
	if err != nil {
		t.Fatalf("filesUnder: %v", err)
	}
	paths := make(map[string]bool, len(files))
	for _, file := range files {
		paths[file.Path] = true
	}
	if !paths["pkg/store.go"] || !paths["lib/lib.go"] || paths["node_modules/dep/dep.go"] {
		t.Fatalf("扫描结果不符合预期: %+v", paths)
	}
}

func TestNewDisabledReturnsNil(t *testing.T) {
	indexer, err := New(config.CodeIndex{Enable: false}, nil)
	if err != nil || indexer != nil {
		t.Fatalf("未启用时应返回 (nil,nil)，得到 (%v,%v)", indexer, err)
	}
	if got := indexer.Tools(); got != nil {
		t.Fatalf("nil 索引器的 Tools 应为 nil，得到 %d 个", len(got))
	}
}

// TestStatusRehydratesFromDB 覆盖"进程重启后仍然认为索引可用"：
// 内存状态是空的，但库里有 indexed_at，就必须回落到 ready。
func TestStatusRehydratesFromDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "code_index.db")
	first, err := New(config.CodeIndex{Enable: true, Path: path}, nil)
	if err != nil || first == nil {
		t.Fatalf("New: %v", err)
	}
	if err := first.store.replaceProject(context.Background(), projectRow{
		ProjectID: testProject,
		Root:      t.TempDir(),
		IndexedAt: 1700000000,
		Truncated: true,
	}, sampleFiles(), sampleSymbols(), sampleEdges()); err != nil {
		t.Fatalf("replaceProject: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := New(config.CodeIndex{Enable: true, Path: path}, nil)
	if err != nil || second == nil {
		t.Fatalf("重新打开: %v", err)
	}
	t.Cleanup(func() {
		_ = second.Close()
	})
	status := second.Status(context.Background(), testProject)
	if status.Status != StatusReady {
		t.Fatalf("重启后状态应为 ready，得到 %+v", status)
	}
	if !status.Truncated || status.FileCount != 3 {
		t.Fatalf("重启后统计应保留，得到 %+v", status)
	}
}
