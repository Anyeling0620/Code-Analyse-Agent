package rag

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"edu.agent.code/config"
)

func symbolsOf(chunks []codeChunk) []string {
	out := make([]string, 0, len(chunks))
	for _, c := range chunks {
		if c.Kind == kindFileSummary {
			continue
		}
		out = append(out, c.Symbol)
	}
	return out
}

func findChunk(chunks []codeChunk, symbol string) (codeChunk, bool) {
	for _, c := range chunks {
		if c.Symbol == symbol {
			return c, true
		}
	}
	return codeChunk{}, false
}

func countKind(chunks []codeChunk, kind string) int {
	n := 0
	for _, c := range chunks {
		if c.Kind == kind {
			n++
		}
	}
	return n
}

// 嵌套闭包不能把外层函数切断。
func TestSplitGoSymbolsKeepsNestedClosureInsideOuter(t *testing.T) {
	filler := strings.Repeat("\tprintln(\"filler\")\n", 20)
	src := "package demo\n\nfunc Outer() {\n\tcb := func() {\n\t\tprintln(\"inner\")\n\t}\n\tcb()\n" + filler + "}\n\nfunc Other() {}\n"

	chunks, ok := splitBySymbol("x.go", src, 1200, 200)
	if !ok {
		t.Fatal("Go 文件应当走符号级切分")
	}
	outer, found := findChunk(chunks, "Outer")
	if !found {
		t.Fatalf("缺少 Outer 块，实际 %v", symbolsOf(chunks))
	}
	if !strings.Contains(outer.Content, "println(\"inner\")") || !strings.Contains(outer.Content, "cb()") {
		t.Error("嵌套闭包被切出 Outer：块内应同时包含内层闭包与其后的语句")
	}
	if _, found := findChunk(chunks, "Other"); !found {
		t.Errorf("Other 应当单独成块，实际 %v", symbolsOf(chunks))
	}
}

// 字符串与注释里的假函数头不能成为块的边界。
func TestSplitGoSymbolsIgnoresFakeDeclarationsInStringsAndComments(t *testing.T) {
	src := "package demo\n\n// func Legacy() 已废弃\nvar doc = `func Fake() {`\n\nfunc Real() {}\n"

	chunks, ok := splitBySymbol("x.go", src, 1200, 200)
	if !ok {
		t.Fatal("Go 文件应当走符号级切分")
	}
	for _, c := range chunks {
		if c.Symbol == "Legacy" || c.Symbol == "Fake" {
			t.Errorf("字符串/注释里的假声明成了独立块：%s", c.Symbol)
		}
	}
	if _, found := findChunk(chunks, "Real"); !found {
		t.Errorf("缺少 Real 块，实际 %v", symbolsOf(chunks))
	}
}

// 超长符号二次切分，且子块保留符号名与行号。
func TestSplitGoSymbolsSplitsOversizedSymbol(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 120; i++ {
		body.WriteString("\tprintln(\"filler line for splitting\")\n")
	}
	src := "package demo\n\nfunc Big() {\n" + body.String() + "}\n"

	const chunkSize = 400
	chunks, ok := splitBySymbol("x.go", src, chunkSize, 60)
	if !ok {
		t.Fatal("Go 文件应当走符号级切分")
	}

	var parts []codeChunk
	for _, c := range chunks {
		if c.Kind == kindFileSummary || c.Symbol != "Big" {
			continue
		}
		parts = append(parts, c)
		if len(c.Content) > chunkSize {
			t.Errorf("子块长度 %d 超出预算 %d", len(c.Content), chunkSize)
		}
		if c.Parent != "Big" {
			t.Errorf("子块应把原符号记为 parent，实际 %q", c.Parent)
		}
	}
	if len(parts) < 2 {
		t.Fatalf("超长函数应当被二次切分，实际 %d 块", len(parts))
	}
	for i := 1; i < len(parts); i++ {
		if parts[i].LineStart <= parts[i-1].LineStart {
			t.Fatalf("子块行号应递增：%d -> %d", parts[i-1].LineStart, parts[i].LineStart)
		}
	}
}

// 相邻的小块合并，避免大量单行块；大块不参与合并。
func TestSplitGoSymbolsMergesTinyAdjacentDecls(t *testing.T) {
	src := "package demo\n\nconst a = 1\n\nconst b = 2\n"

	chunks, ok := splitBySymbol("x.go", src, 1200, 200)
	if !ok {
		t.Fatal("Go 文件应当走符号级切分")
	}
	if n := countKind(chunks, kindConst); n != 1 {
		t.Fatalf("相邻的小常量块应当合并成 1 块，实际 %d 块（%v）", n, symbolsOf(chunks))
	}
	merged := chunks[len(chunks)-1]
	if !strings.Contains(merged.Content, "const a = 1") || !strings.Contains(merged.Content, "const b = 2") {
		t.Errorf("合并后的块应当同时包含两个声明，实际 %q", merged.Content)
	}
}

// 每个 Go 文件都要有文件摘要块，且摘要是结构化的（包名/import/声明清单）。
func TestSplitGoSymbolsProducesFileSummary(t *testing.T) {
	src := "// Package demo 是示例。\npackage demo\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\ntype Thing struct{ N int }\n\nfunc Use() { fmt.Println(os.Args) }\n"

	chunks, ok := splitBySymbol("x.go", src, 1200, 200)
	if !ok {
		t.Fatal("Go 文件应当走符号级切分")
	}
	if len(chunks) == 0 || chunks[0].Kind != kindFileSummary {
		t.Fatalf("第一个块应当是文件摘要，实际 %v", chunks)
	}
	summary := chunks[0].Content
	for _, want := range []string{"package demo", "\"fmt\"", "type Thing", "func Use"} {
		if !strings.Contains(summary, want) {
			t.Errorf("文件摘要缺少 %q，实际内容：\n%s", want, summary)
		}
	}
}

// 语法错误的文件不能 panic，也不该整份丢掉。
func TestSplitGoSymbolsSurvivesSyntaxError(t *testing.T) {
	src := "package demo\n\nfunc Fine() {\n\tprintln(1)\n}\n\nfunc Broken() {\n\tif\n}\n"

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("切分不应 panic：%v", r)
		}
	}()
	chunks, ok := splitBySymbol("x.go", src, 1200, 200)
	if !ok {
		return // 解析器若完全拒绝，回退到通用切分器也是可接受的
	}
	if _, found := findChunk(chunks, "Fine"); !found {
		t.Errorf("部分 AST 应当保住可解析的声明，实际 %v", symbolsOf(chunks))
	}
}

// 大括号语言：字符串里的花括号不能打断块边界。
func TestSplitBraceSymbolsSplitsTopLevelBlocks(t *testing.T) {
	pad := strings.Repeat("\t\t// filler\n", 20)
	src := "export class Alpha {\n\trun() {\n" + pad + "\t\tconst s = \"}{\";\n\t\treturn s;\n\t}\n}\n\n" +
		"export class Beta {\n\trun() {\n" + pad + "\t\treturn 1;\n\t}\n}\n"

	chunks, ok := splitBySymbol("x.ts", src, 1200, 200)
	if !ok {
		t.Fatal("TypeScript 文件应当走大括号配对切分")
	}
	for _, want := range []string{"Alpha", "Beta"} {
		if _, found := findChunk(chunks, want); !found {
			t.Fatalf("缺少 %s 块，实际 %v", want, symbolsOf(chunks))
		}
	}
	// 字符串里的花括号不打断边界：它应当完整落在 Alpha.run 这一块里。
	run, found := findChunk(chunks, "Alpha.run")
	if !found {
		t.Fatalf("缺少 Alpha.run 块，实际 %v", symbolsOf(chunks))
	}
	if !strings.Contains(run.Content, `"}{"`) {
		t.Error("字符串里的花括号不应打断块边界")
	}
	if run.Parent != "Alpha" {
		t.Errorf("方法块的 Parent 应为 Alpha，实际 %q", run.Parent)
	}
}

// 类内部的每个方法各自成块：这是"方法级检索"的前提。
func TestSplitBraceSymbolsExtractsClassMembers(t *testing.T) {
	pad := strings.Repeat("\t\t// filler\n", 20)
	src := "export class Client {\n" +
		"\tconstructor(baseURL) {\n" + pad + "\t\tthis.baseURL = baseURL;\n\t}\n\n" +
		"\tasync request(config) {\n" + pad + "\t\treturn config;\n\t}\n\n" +
		"\tget(url, options) {\n" + pad + "\t\treturn url;\n\t}\n" +
		"}\n"

	chunks, ok := splitBySymbol("x.js", src, 1200, 200)
	if !ok {
		t.Fatal("JavaScript 文件应当走大括号配对切分")
	}
	for _, want := range []string{"Client", "Client.constructor", "Client.request", "Client.get"} {
		if _, found := findChunk(chunks, want); !found {
			t.Fatalf("缺少 %s 块，实际 %v", want, symbolsOf(chunks))
		}
	}
	for _, want := range []string{"constructor", "request", "get"} {
		c, _ := findChunk(chunks, "Client."+want)
		if c.Kind != kindMethod {
			t.Errorf("%s 的 kind 应为 %s，实际 %s", c.Symbol, kindMethod, c.Kind)
		}
		if c.Parent != "Client" {
			t.Errorf("%s 的 Parent 应为 Client，实际 %q", c.Symbol, c.Parent)
		}
	}
	// 头部块只覆盖类声明，不应把方法体也吞进去。
	header, _ := findChunk(chunks, "Client")
	if strings.Contains(header.Content, "this.baseURL = baseURL") {
		t.Error("头部块不应包含方法体（否则方法会被重复索引）")
	}
}

// 方法体内的嵌套闭包不能变成新的成员块。
func TestSplitBraceSymbolsKeepsNestedClosureInsideMethod(t *testing.T) {
	pad := strings.Repeat("\t\t// filler\n", 20)
	src := "class Worker {\n" +
		"\trun() {\n" + pad + "\t\tconst cb = () => {\n\t\t\treturn 1;\n\t\t};\n\t\tcb();\n\t}\n" +
		"}\n"

	chunks, ok := splitBySymbol("x.js", src, 1200, 200)
	if !ok {
		t.Fatal("JavaScript 文件应当走大括号配对切分")
	}
	run, found := findChunk(chunks, "Worker.run")
	if !found {
		t.Fatalf("缺少 Worker.run 块，实际 %v", symbolsOf(chunks))
	}
	if !strings.Contains(run.Content, "cb()") || !strings.Contains(run.Content, "const cb") {
		t.Error("嵌套闭包被切出 run：块内应同时包含闭包定义与其后的语句")
	}
	for _, c := range chunks {
		if c.Kind == kindMethod && c.Symbol != "Worker.run" {
			t.Errorf("闭包不应成为独立方法块，实际 %q", c.Symbol)
		}
	}
}

// 类字段里的对象字面量不是方法：既不能成块，也不能把后面的方法吞掉。
func TestSplitBraceSymbolsIgnoresObjectLiteralMembers(t *testing.T) {
	pad := strings.Repeat("\t\t// filler\n", 20)
	src := "class Config {\n" +
		"\tstatic defaults = {\n\t\ttimeout: 1,\n\t\tretries: 2,\n\t};\n\n" +
		"\tload() {\n" + pad + "\t\treturn this.defaults;\n\t}\n" +
		"}\n"

	chunks, ok := splitBySymbol("x.js", src, 1200, 200)
	if !ok {
		t.Fatal("JavaScript 文件应当走大括号配对切分")
	}
	if _, found := findChunk(chunks, "Config.load"); !found {
		t.Fatalf("缺少 Config.load 块，实际 %v", symbolsOf(chunks))
	}
	for _, c := range chunks {
		if c.Kind == kindMethod && c.Symbol != "Config.load" {
			t.Errorf("对象字面量被误判成方法：%q（内容 %.40q）", c.Symbol, c.Content)
		}
	}
	// 字段声明不能丢：它应当落在头部块里。
	header, _ := findChunk(chunks, "Config")
	if !strings.Contains(header.Content, "static defaults") {
		t.Error("字段声明被丢弃：应保留在容器头部块里")
	}
}

// 没有方法成员的容器（如 TS 的 interface）保持原来的整块行为。
func TestSplitBraceSymbolsWithoutMembersKeepsBlock(t *testing.T) {
	src := "export interface Options {\n\turl: string;\n\tmethod: string;\n}\n"

	chunks, ok := splitBySymbol("x.ts", src, 1200, 200)
	if !ok {
		t.Fatal("TypeScript 文件应当走大括号配对切分")
	}
	if _, found := findChunk(chunks, "Options"); !found {
		t.Fatalf("缺少 Options 块，实际 %v", symbolsOf(chunks))
	}
	for _, c := range chunks {
		if c.Kind == kindMethod {
			t.Errorf("interface 不应产出方法块：%q", c.Symbol)
		}
	}
}

// 容器拆块不能丢内容：类体里每一行非空文本都要落在某个块里。
func TestSplitBraceSymbolsPreservesAllContent(t *testing.T) {
	pad := strings.Repeat("\t\t// filler\n", 20)
	src := "class Mixed {\n" +
		"\tfield = 1;\n\n" +
		"\tfirst() {\n" + pad + "\t\treturn 1;\n\t}\n\n" +
		"\tsecond = async (a) => {\n" + pad + "\t\treturn a;\n\t};\n\n" +
		"\tthird() {\n" + pad + "\t\treturn 3;\n\t}\n" +
		"}\n"

	chunks, ok := splitBySymbol("x.js", src, 1200, 200)
	if !ok {
		t.Fatal("JavaScript 文件应当走大括号配对切分")
	}
	var joined strings.Builder
	for _, c := range chunks {
		if c.Kind == kindFileSummary {
			continue
		}
		joined.WriteString(c.Content)
		joined.WriteString("\n")
	}
	all := joined.String()
	for _, want := range []string{"field = 1;", "return 1;", "return a;", "return 3;", "second = async"} {
		if !strings.Contains(all, want) {
			t.Errorf("内容丢失：块集合里找不到 %q", want)
		}
	}
}

// 缩进语言：按顶层 def/class 切分。
func TestSplitIndentSymbolsSplitsPythonDefs(t *testing.T) {
	pad := strings.Repeat("    x = 1\n", 40)
	src := "import os\n\n\ndef alpha():\n" + pad + "    return 1\n\n\ndef beta():\n" + pad + "    return 2\n"

	chunks, ok := splitBySymbol("x.py", src, 1200, 200)
	if !ok {
		t.Fatal("Python 文件应当走缩进切分")
	}
	for _, want := range []string{"alpha", "beta"} {
		if _, found := findChunk(chunks, want); !found {
			t.Fatalf("缺少 %s 块，实际 %v", want, symbolsOf(chunks))
		}
	}
}

// 不支持的语言必须回退，交给调用方的通用切分器。
func TestSplitBySymbolDeclinesUnknownLanguage(t *testing.T) {
	for _, path := range []string{"notes.txt", "README", "data.json", "schema.sql"} {
		if _, ok := splitBySymbol(path, "some content here", 1200, 200); ok {
			t.Errorf("%s 不应走符号级切分", path)
		}
	}
}

// 同样输入必须产出同样结果：增量索引依赖内容哈希短路。
func TestSplitBySymbolIsDeterministic(t *testing.T) {
	src := "package demo\n\nimport \"fmt\"\n\nconst a = 1\n\nconst b = 2\n\nfunc Use() { fmt.Println(a, b) }\n\ntype Thing struct{ N int }\n"

	first, ok := splitBySymbol("x.go", src, 1200, 200)
	if !ok {
		t.Fatal("Go 文件应当走符号级切分")
	}
	for i := 0; i < 3; i++ {
		again, _ := splitBySymbol("x.go", src, 1200, 200)
		if len(again) != len(first) {
			t.Fatalf("第 %d 次切分块数不同：%d vs %d", i, len(again), len(first))
		}
		for j := range first {
			if again[j].Content != first[j].Content || again[j].Symbol != first[j].Symbol ||
				again[j].LineStart != first[j].LineStart {
				t.Fatalf("第 %d 次切分第 %d 块不一致", i, j)
			}
		}
	}
}

// 端到端：索引一个 Go 文件时，符号与行号要真的落到 chunk 元数据里。
func TestLoadDocsInScopeAddsSymbolMetadata(t *testing.T) {
	root := t.TempDir()
	src := "package demo\n\nfunc Use() { println(1) }\n"
	if err := os.WriteFile(filepath.Join(root, "use.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	docs, err := loadDocsInScope(context.Background(), root,
		config.RAG{ChunkSize: 1200, ChunkOverlap: 200, MaxFileBytes: 1 << 20},
		docScope{ProjectID: "p1", ProjectRoot: root, Commit: "c1"})
	if err != nil {
		t.Fatalf("loadDocsInScope: %v", err)
	}

	var sawFunc, sawSummary bool
	for _, doc := range docs {
		switch doc.MetaData["kind"] {
		case kindFileSummary:
			sawSummary = true
		case kindFunc:
			if doc.MetaData["symbol"] != "Use" {
				continue
			}
			sawFunc = true
			if _, ok := doc.MetaData["line_start"]; !ok {
				t.Error("符号块缺少 line_start 元数据")
			}
			if _, ok := doc.MetaData["line_end"]; !ok {
				t.Error("符号块缺少 line_end 元数据")
			}
			if header, _ := doc.MetaData["header"].(string); !strings.Contains(header, "Use") {
				t.Errorf("header 应当描述块内容，实际 %q", header)
			}
		}
	}
	if !sawFunc {
		t.Fatal("未产出 func Use 的符号块")
	}
	if !sawSummary {
		t.Error("未产出文件摘要块")
	}
}

// 真实输入的性质检查：拿本仓库自己的 Go 文件跑一遍，
// 确认行号落在文件范围内、块内容确实摘自原文。
func TestSplitBySymbolOnRealRepoFiles(t *testing.T) {
	for _, name := range []string{"chunker.go", "service.go", "project.go"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(data)
		lineCount := strings.Count(src, "\n") + 1

		chunks, ok := splitBySymbol(name, src, 1200, 200)
		if !ok {
			t.Fatalf("%s 应当能按符号切分", name)
		}

		symbols := 0
		for _, c := range chunks {
			if strings.TrimSpace(c.Content) == "" {
				t.Errorf("%s 产出了空块", name)
			}
			if c.Kind == kindFileSummary {
				continue
			}
			symbols++
			if c.LineStart < 1 || c.LineEnd < c.LineStart || c.LineEnd > lineCount {
				t.Errorf("%s 行号越界：%d-%d（文件共 %d 行）", name, c.LineStart, c.LineEnd, lineCount)
			}
			if !strings.Contains(src, strings.TrimRight(c.Content, " \t\r\n")) {
				t.Errorf("%s 的块内容不是原文片段：%.40q", name, c.Content)
			}
			if len(c.Content) > 1200 {
				t.Errorf("%s 的块 %q 长度 %d 超过预算", name, c.Symbol, len(c.Content))
			}
		}
		if symbols == 0 {
			t.Errorf("%s 没有切出任何符号块", name)
		}
		t.Logf("%s: %d 行 -> %d 块（含文件摘要）", name, lineCount, len(chunks))
	}
}

// 真实仓库全量回归：对本地所有克隆里的代码文件跑一遍切分，
// 要求不 panic、不产出空块、行号在范围内、块内容必须是原文片段。
//
// 这条能抓住"某个语言的某种写法把区间算错"这类问题：单元测试的
// 构造样例覆盖不到真实代码的写法组合（例如相邻顶层块重叠后起点被夹进
// 开括号内部，曾导致切片越界 panic）。
func TestSplitBySymbolAcrossLocalClones(t *testing.T) {
	root := filepath.Join("..", "..", "workspace", "repos")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("没有本地克隆可回归：%v", err)
	}
	checked := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		if _, ok := codeLanguages[strings.ToLower(filepath.Ext(p))]; !ok {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > 512*1024 {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		src := string(data)
		lineCount := strings.Count(src, "\n") + 1
		chunks, ok := splitBySymbol(p, src, 1200, 200)
		if !ok {
			return nil
		}
		checked++
		for _, c := range chunks {
			// file_summary 是合成块（Go 那份会自己拼一行 "// file summary"），
			// 不保证是原文片段，只对符号块做切片一致性检查。
			if c.Kind == kindFileSummary {
				continue
			}
			text := strings.TrimRight(c.Content, " \t\r\n")
			if strings.TrimSpace(text) == "" {
				t.Errorf("%s: 产出空块", p)
				continue
			}
			if c.LineStart < 1 || c.LineEnd < c.LineStart || c.LineEnd > lineCount {
				t.Errorf("%s: 行号越界 %d-%d（共 %d 行）", p, c.LineStart, c.LineEnd, lineCount)
			}
			if !strings.Contains(src, text) {
				t.Errorf("%s: 块内容不是原文片段：%.40q", p, c.Content)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Skip("没有可回归的源文件")
	}
	t.Logf("回归了 %d 个源文件", checked)
}
