package rag

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"edu.agent.code/config"
)

// 锁文件与构建产物必须在取样阶段就被拦掉。
func TestSkippedArtifacts(t *testing.T) {
	skip := []string{
		"web/package-lock.json", "web/pnpm-lock.yaml", "yarn.lock", "go.sum",
		"web/dist/app.min.js", "web/dist/app.js.map", "composer.lock",
		"some/deep/Cargo.lock",
	}
	keep := []string{
		"service/rag/service.go", "web/src/api/client.ts", "docker-compose.yml",
		"README.md", "go.mod", "web/package.json", "web/src/main.tsx",
		"docs/mapping.md",
	}
	for _, p := range skip {
		if !isSkippedArtifact(p) {
			t.Errorf("应当跳过: %s", p)
		}
	}
	for _, p := range keep {
		if isSkippedArtifact(p) {
			t.Errorf("不应跳过: %s", p)
		}
	}
}

// import 清单不是代码块：历史上它会被截成 `import type { APIResponse }`，
// 块尾落在行中间。
func TestTSImportListIsNotABlock(t *testing.T) {
	src := "import type { APIResponse, ChatMessage } from '../types/chat';\n" +
		"import { request } from './request';\n" +
		"import {\n" +
		"  helperA,\n" +
		"  helperB,\n" +
		"} from './helpers';\n" +
		"\n" +
		"export async function fetchChat(payload: unknown): Promise<APIResponse> {\n" +
		"  const res = await request({ body: { payload } });\n" +
		"  return res;\n" +
		"}\n"

	chunks, ok := splitBySymbol("client.ts", src, 1200, 200)
	if !ok {
		t.Fatal("TS 文件应当走符号切分")
	}
	if _, found := findChunk(chunks, "fetchChat"); !found {
		t.Errorf("缺少 fetchChat 块，实际 %v", symbolsOf(chunks))
	}
	for _, c := range chunks {
		if c.Kind == kindFileSummary {
			continue
		}
		if strings.Contains(c.Content, "import type {") || strings.Contains(c.Content, "from './helpers'") {
			t.Errorf("import 清单不应单独成块: %q", firstLineOf(c.Content))
		}
	}
}

// 顶层块必须以整行结束：块尾不能落在行中间。
// 历史上 ts/tsx 有 48% 的块在行中间截断，根因是 import 清单、
// 类型字面量、解构这些"同行开闭"的花括号被当成了代码块。
func TestBraceChunksEndOnLineBoundary(t *testing.T) {
	src := "import type { APIResponse } from '../types/chat';\n" +
		"\n" +
		"interface Props {\n" +
		"  title: string;\n" +
		"  onClose: () => void;\n" +
		"}\n" +
		"\n" +
		"const styles: Record<string, string> = {\n" +
		"  overlay: 'fixed',\n" +
		"  card: 'rounded',\n" +
		"};\n" +
		"\n" +
		"export default function App({ title, onClose }: Props) {\n" +
		"  const [open, setOpen] = useState(false);\n" +
		"  return (\n" +
		"    <div className={styles.overlay}>\n" +
		"      {open && <span>{title}</span>}\n" +
		"    </div>\n" +
		"  );\n" +
		"}\n"

	chunks, ok := splitBySymbol("App.tsx", src, 1200, 200)
	if !ok {
		t.Fatal("TSX 文件应当走符号切分")
	}
	for _, c := range chunks {
		if c.Kind == kindText {
			continue
		}
		if c.endByte > 0 && c.endByte < len(src) && src[c.endByte] != '\n' {
			t.Errorf("块尾落在行中间: %q", firstLineOf(c.Content))
		}
	}
}

// 评测/夹具类目录必须在遍历阶段整目录跳过。
// 回归背景：评测问题集文件被索引进项目集合后，查询与语料逐字重合，
// 45 题里有 17 题的第一名是这个问题集文件自己，而非答案。
func TestSkippedDirs(t *testing.T) {
	skip := []string{
		"eval", "Eval", "EVAL",
		"testdata", "fixtures", "benchmarks", "benchmark",
		"node_modules", "vendor", "dist", "build", "target", "data",
		".git", ".idea", ".vscode", ".obsidian",
	}
	keep := []string{
		"service", "internal", "src", "api", "adaptor", "web", "scripts",
		"docs", "tests", "test", "migrations", "cmd",
	}
	for _, name := range skip {
		if !shouldSkipDir(name) {
			t.Errorf("应当整目录跳过: %s", name)
		}
	}
	for _, name := range keep {
		if shouldSkipDir(name) {
			t.Errorf("不应跳过: %s", name)
		}
	}
}

// 端到端验证：走真实取样管线，确认 eval/ 与 testdata/ 被整目录跳过，
// 而同名的普通源码目录不受影响。单元测试只证明判定函数对，
// 这条才证明遍历时真的没走进去。
func TestLoadDocsInScopeSkipsEvalDirs(t *testing.T) {
	if testing.Short() {
		t.Skip("walks filesystem")
	}
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	// 应当被跳过的目录。
	write("eval/rag/queryset.json", `["问题一：鉴权在哪实现？","问题二：成本怎么算？"]`)
	write("testdata/fixture.json", `{"fixture":true}`)
	write("fixtures/sample.json", `{"sample":true}`)
	write("benchmarks/bench.json", `{"bench":true}`)
	// 应当被保留的产品代码。
	write("main.go", "package main\n\nfunc main() {}\n")
	write("service/rag/service.go", "package rag\n\nfunc Run() {}\n")

	conf := config.RAG{ChunkSize: 1200, ChunkOverlap: 200, MaxFileBytes: 10 << 20}
	docs, err := loadDocsInScope(context.Background(), root, conf, docScope{ProjectID: "ptest", ProjectRoot: root})
	if err != nil {
		t.Fatalf("loadDocsInScope: %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("取样结果为空，连产品代码都没收到")
	}
	var paths []string
	for _, d := range docs {
		src := ""
		if d.MetaData != nil {
			src, _ = d.MetaData["source_path"].(string)
		}
		paths = append(paths, src)
		for _, banned := range []string{"eval/", "testdata/", "fixtures/", "benchmarks/"} {
			if strings.HasPrefix(src, banned) {
				t.Errorf("被跳过目录的文件仍被索引: %s", src)
			}
		}
	}
	joined := strings.Join(paths, ",")
	for _, want := range []string{"main.go", "service/rag/service.go"} {
		if !strings.Contains(joined, want) {
			t.Errorf("产品代码未被索引: %s（实际 %s）", want, joined)
		}
	}
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 90 {
		s = s[:90]
	}
	return s
}
