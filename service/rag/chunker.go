package rag

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
)

// 切分产出的块。Content 是块的原文；其余字段用于回填元数据与引用回溯。
type codeChunk struct {
	Content   string
	Symbol    string
	Kind      string
	Parent    string
	LineStart int
	LineEnd   int
	startByte int
	endByte   int
}

const (
	kindFileSummary = "file_summary"
	kindFunc        = "func"
	kindMethod      = "method"
	kindStruct      = "struct"
	kindInterface   = "interface"
	kindType        = "type"
	kindConst       = "const"
	kindVar         = "var"
	kindBlock       = "block"
	kindText        = "text"

	// mergeBelow 以下的符号块会与相邻同类型块合并，
	// 避免文件里堆满"单行 getter / 单个常量"这种低信息量块。
	mergeBelow = 200
	// maxSummaryNames 是文件摘要块里最多列出的顶层声明名数量。
	maxSummaryNames = 40
)

// codeLanguages 把扩展名映射到切分策略。
// 未登记的类型返回 false，调用方回退到原来的通用递归切分器。
var codeLanguages = map[string]string{
	".go": "go",

	".java": "brace", ".kt": "brace", ".kts": "brace",
	".js": "brace", ".jsx": "brace", ".mjs": "brace", ".cjs": "brace",
	".ts": "brace", ".tsx": "brace", ".vue": "brace",
	".c": "brace", ".h": "brace", ".cc": "brace", ".cpp": "brace", ".hpp": "brace",
	".cs": "brace", ".php": "brace", ".swift": "brace", ".scala": "brace",
	".rs": "brace",

	".py": "indent", ".rb": "indent",
}

// splitBySymbol 按源码结构切分 content。
// 第二个返回值为 false 表示该文件无法按符号切分（语言不支持、解析不出来、
// 或切分结果为空），调用方应回退到通用递归切分器并保持原有行为。
func splitBySymbol(relPath, content string, chunkSize, chunkOverlap int) ([]codeChunk, bool) {
	lang, ok := codeLanguages[strings.ToLower(filepath.Ext(relPath))]
	if !ok || strings.TrimSpace(content) == "" {
		return nil, false
	}

	var chunks []codeChunk
	switch lang {
	case "go":
		chunks = splitGoSymbols(content)
	case "brace":
		chunks = splitBraceSymbols(content)
	case "indent":
		chunks = splitIndentSymbols(content)
	}
	if len(chunks) == 0 {
		return nil, false
	}

	chunks = mergeSmallChunks(chunks, content, chunkSize)
	chunks = splitOversizedChunks(chunks, chunkSize, chunkOverlap)
	if len(chunks) == 0 {
		return nil, false
	}
	return chunks, true
}

// ---------- Go：用标准库 AST 拿到精确的字节区间 ----------

func splitGoSymbols(content string) []codeChunk {
	fset := token.NewFileSet()
	// 语法有误的文件同样会返回部分 AST，这里刻意忽略 err：
	// 能拿到多少声明就索引多少，好过整份文件退回盲切。
	file, _ := parser.ParseFile(fset, "chunk.go", content, parser.ParseComments)
	if file == nil {
		return nil
	}

	chunks := make([]codeChunk, 0, len(file.Decls)+1)
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			start := d.Pos()
			if d.Doc != nil {
				start = d.Doc.Pos()
			}
			kind, symbol, parent := kindFunc, d.Name.Name, ""
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = kindMethod
				if recv := receiverTypeName(d.Recv.List[0].Type); recv != "" {
					parent = recv
					symbol = recv + "." + d.Name.Name
				}
			}
			if c, ok := makeChunk(content, fset, start, d.End(), symbol, kind, parent); ok {
				chunks = append(chunks, c)
			}
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				// import 归入文件摘要块，不单独成块。
				continue
			}
			kind := kindType
			switch d.Tok {
			case token.CONST:
				kind = kindConst
			case token.VAR:
				kind = kindVar
			case token.TYPE:
				kind = genDeclTypeKind(d)
			}
			start := d.Pos()
			if d.Doc != nil {
				start = d.Doc.Pos()
			}
			if c, ok := makeChunk(content, fset, start, d.End(), genDeclSymbol(d), kind, ""); ok {
				chunks = append(chunks, c)
			}
		}
	}
	if len(chunks) == 0 {
		return nil
	}
	if summary, ok := goFileSummary(file); ok {
		chunks = append([]codeChunk{summary}, chunks...)
	}
	return chunks
}

func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return receiverTypeName(t.X)
	case *ast.IndexListExpr:
		return receiverTypeName(t.X)
	case *ast.ParenExpr:
		return receiverTypeName(t.X)
	default:
		return ""
	}
}

func genDeclTypeKind(d *ast.GenDecl) string {
	if len(d.Specs) == 1 {
		if ts, ok := d.Specs[0].(*ast.TypeSpec); ok {
			switch ts.Type.(type) {
			case *ast.StructType:
				return kindStruct
			case *ast.InterfaceType:
				return kindInterface
			}
		}
	}
	return kindType
}

func genDeclSymbol(d *ast.GenDecl) string {
	names := make([]string, 0, len(d.Specs))
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			names = append(names, s.Name.Name)
		case *ast.ValueSpec:
			for _, n := range s.Names {
				names = append(names, n.Name)
			}
		}
	}
	if len(names) > 3 {
		names = append(names[:3], "…")
	}
	return strings.Join(names, ", ")
}

// goFileSummary 生成每个 Go 文件必备的摘要块：
// 包名 + import + 顶层声明名清单，用于粗定位和父块上下文。
func goFileSummary(file *ast.File) (codeChunk, bool) {
	var sb strings.Builder
	sb.WriteString("// file summary\n")
	if file.Doc != nil {
		if doc := strings.TrimSpace(file.Doc.Text()); doc != "" {
			sb.WriteString(doc)
			sb.WriteString("\n")
		}
	}
	if file.Name != nil {
		sb.WriteString("package " + file.Name.Name + "\n")
	}
	if len(file.Imports) > 0 {
		paths := make([]string, 0, len(file.Imports))
		for _, imp := range file.Imports {
			paths = append(paths, imp.Path.Value)
		}
		sb.WriteString("imports: " + strings.Join(paths, ", ") + "\n")
	}

	names := make([]string, 0, len(file.Decls))
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				if recv := receiverTypeName(d.Recv.List[0].Type); recv != "" {
					name = recv + "." + name
				}
			}
			names = append(names, "func "+name)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					names = append(names, "type "+ts.Name.Name)
				}
			}
		}
	}
	if len(names) > maxSummaryNames {
		names = append(names[:maxSummaryNames], "…")
	}
	if len(names) > 0 {
		sb.WriteString("declares: " + strings.Join(names, ", ") + "\n")
	}

	text := strings.TrimSpace(sb.String())
	if text == "" {
		return codeChunk{}, false
	}
	return codeChunk{Content: text, Kind: kindFileSummary, LineStart: 1, LineEnd: 1}, true
}

func makeChunk(content string, fset *token.FileSet, start, end token.Pos, symbol, kind, parent string) (codeChunk, bool) {
	sp, ep := fset.Position(start), fset.Position(end)
	if sp.Offset < 0 || ep.Offset > len(content) || sp.Offset >= ep.Offset {
		return codeChunk{}, false
	}
	text := strings.TrimRight(content[sp.Offset:ep.Offset], " \t\r\n")
	if strings.TrimSpace(text) == "" {
		return codeChunk{}, false
	}
	return codeChunk{
		Content:   text,
		Symbol:    symbol,
		Kind:      kind,
		Parent:    parent,
		LineStart: sp.Line,
		LineEnd:   sp.Line + strings.Count(text, "\n"),
		startByte: sp.Offset,
		endByte:   ep.Offset,
	}, true
}

// ---------- 大括号语言：按顶层 { } 配对切块 ----------

// splitBraceSymbols 用"深度归零"定位顶层代码块，
// 不依赖各语言的声明关键字，扫描时跳过字符串与注释，避免被其中的花括号带偏。
func splitBraceSymbols(content string) []codeChunk {
	li := newLineIndex(content)
	type span struct{ start, end int }
	var spans []span

	depth, blockStart, openIdx := 0, 0, 0
	for i := 0; i < len(content); {
		switch content[i] {
		case '/':
			if i+1 < len(content) && content[i+1] == '/' {
				for i < len(content) && content[i] != '\n' {
					i++
				}
				continue
			}
			if i+1 < len(content) && content[i+1] == '*' {
				i += 2
				for i+1 < len(content) && !(content[i] == '*' && content[i+1] == '/') {
					i++
				}
				if i+1 < len(content) {
					i += 2
				} else {
					i = len(content)
				}
				continue
			}
			i++
		case '"', '\'', '`':
			quote := content[i]
			i++
			for i < len(content) {
				if content[i] == '\\' && quote != '`' {
					i += 2
					continue
				}
				if content[i] == quote {
					i++
					break
				}
				i++
			}
		case '{':
			if depth == 0 {
				blockStart = blockStartOffset(content, i, li)
				openIdx = i
			}
			depth++
			i++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 {
					if start, end, ok := braceBlockSpan(content, blockStart, openIdx, i, li); ok {
						spans = append(spans, span{start, end})
					}
				}
			}
			i++
		default:
			i++
		}
	}
	if len(spans) == 0 {
		return nil
	}

	chunks := make([]codeChunk, 0, len(spans)+1)
	if summary, ok := prefixSummary(content, spans[0].start, li); ok {
		chunks = append(chunks, summary)
	}
	lastEnd := 0
	for _, s := range spans {
		// 扩到行尾后相邻块可能重叠，裁掉已归入上一块的部分。
		if s.start < lastEnd {
			s.start = lastEnd
		}
		if s.start >= s.end {
			continue
		}
		text := strings.TrimRight(content[s.start:s.end], " \t\r\n")
		if strings.TrimSpace(text) == "" {
			continue
		}
		symbol, kind := guessSymbol(text)
		chunks = append(chunks, codeChunk{
			Content:   text,
			Symbol:    symbol,
			Kind:      kind,
			LineStart: li.lineOf(s.start),
			LineEnd:   li.lineOf(s.end - 1),
			startByte: s.start,
			endByte:   s.end,
		})
		lastEnd = s.end
	}
	return chunks
}

// braceBlockSpan 判断一对顶层花括号是否真的是代码块，并把它扩展到行尾。
//
// 只按"深度归零"取块在 TS/JS 上会大量误判：import 清单、类型字面量、
// 解构赋值、对象字面量里的花括号同样出现在顶层深度，却只覆盖半行内容，
// 于是产出大量"从行中间截断"的碎片块（实测 ts/tsx 有 48% 的块如此结尾）。
func braceBlockSpan(content string, blockStart, openIdx, closeIdx int, li *lineIndex) (int, int, bool) {
	// 同一行开闭的花括号一定不是语句块。
	if li.lineOf(openIdx) == li.lineOf(closeIdx) {
		return 0, 0, false
	}
	if isImportBrace(content, openIdx, closeIdx, li) {
		return 0, 0, false
	}
	// 块尾扩到行尾，把 `};`、尾随注释一并纳入，避免落在行中间。
	end := li.offsetOfLineEnd(closeIdx)
	if end <= blockStart {
		return 0, 0, false
	}
	return blockStart, end, true
}

// isImportBrace 识别模块导入/导出清单的花括号：
// import { A, B } from './x'、export type { T } from './x'。
// 这类括号里的名字属于文件头，交给前缀摘要块，不单独成块。
func isImportBrace(content string, openIdx, closeIdx int, li *lineIndex) bool {
	prefix := strings.TrimSpace(content[li.offsetOfLineStart(openIdx):openIdx])
	if strings.HasPrefix(prefix, "import") {
		return true
	}
	// export { a } from './x'：只有紧跟 from 才算导入清单，
	// 否则 export function foo() { 会被误伤。
	if strings.HasPrefix(prefix, "export") && !strings.Contains(prefix, "=") {
		rest := strings.TrimSpace(content[closeIdx+1 : li.offsetOfLineEnd(closeIdx)])
		return strings.HasPrefix(rest, "from")
	}
	return false
}

var braceSymbolPatterns = []struct {
	re   *regexp.Regexp
	kind string
}{
	{regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s+([A-Za-z_$][\w$]*)`), kindFunc},
	{regexp.MustCompile(`(?m)^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?fn\s+([A-Za-z_]\w*)`), kindFunc},
	{regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:default\s+)?(?:pub(?:\([^)]*\))?\s+)?(?:public|private|protected|internal|abstract|sealed|open|final|partial|static|strictfp|\s)*\b(?:class|interface|enum|record|trait|impl|object|struct)\s+([A-Za-z_]\w*)`), kindType},
}

// guessSymbol 只用于补元数据，不参与边界判定：猜不出来就退化为 block。
func guessSymbol(text string) (string, string) {
	for _, p := range braceSymbolPatterns {
		if m := p.re.FindStringSubmatch(text); len(m) > 1 {
			return m[1], p.kind
		}
	}
	return "", kindBlock
}

// blockStartOffset 把块的起点回溯到声明所在行的行首，并并入紧邻的注释行。
func blockStartOffset(content string, braceIdx int, li *lineIndex) int {
	start := li.offsetOfLineStart(braceIdx)
	for start > 0 {
		prevStart := li.offsetOfLineStart(start - 1)
		if prevStart >= start-1 {
			break
		}
		prevLine := strings.TrimSpace(content[prevStart : start-1])
		if strings.HasPrefix(prevLine, "//") || strings.HasPrefix(prevLine, "/*") || strings.HasPrefix(prevLine, "*") {
			start = prevStart
			continue
		}
		break
	}
	return start
}

// prefixSummary 把第一个顶层块之前的文件头（package/import/注释）做成摘要块。
func prefixSummary(content string, firstStart int, li *lineIndex) (codeChunk, bool) {
	if firstStart <= 0 || firstStart > len(content) {
		return codeChunk{}, false
	}
	text := strings.TrimRight(content[:firstStart], " \t\r\n")
	if strings.TrimSpace(text) == "" {
		return codeChunk{}, false
	}
	return codeChunk{
		Content:   text,
		Kind:      kindFileSummary,
		LineStart: 1,
		LineEnd:   li.lineOf(firstStart - 1),
	}, true
}

// ---------- 缩进语言（Python / Ruby）----------

var indentDeclRe = regexp.MustCompile(`(?m)^([ \t]*)(?:async\s+)?(?:def|class|module)\s+([A-Za-z_]\w*)`)

func splitIndentSymbols(content string) []codeChunk {
	matches := indentDeclRe.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return nil
	}

	minIndent := -1
	for _, m := range matches {
		if indent := m[3] - m[2]; minIndent < 0 || indent < minIndent {
			minIndent = indent
		}
	}

	li := newLineIndex(content)
	type decl struct {
		start  int
		symbol string
	}
	var decls []decl
	for _, m := range matches {
		if m[3]-m[2] != minIndent {
			continue
		}
		decls = append(decls, decl{start: m[0], symbol: content[m[4]:m[5]]})
	}
	if len(decls) == 0 {
		return nil
	}

	chunks := make([]codeChunk, 0, len(decls)+1)
	if summary, ok := prefixSummary(content, decls[0].start, li); ok {
		chunks = append(chunks, summary)
	}
	for i, d := range decls {
		end := len(content)
		if i+1 < len(decls) {
			end = decls[i+1].start
		}
		text := strings.TrimRight(content[d.start:end], " \t\r\n")
		if strings.TrimSpace(text) == "" {
			continue
		}
		kind := kindFunc
		if strings.HasPrefix(strings.TrimSpace(content[d.start:]), "class") {
			kind = kindType
		}
		chunks = append(chunks, codeChunk{
			Content:   text,
			Symbol:    d.symbol,
			Kind:      kind,
			LineStart: li.lineOf(d.start),
			LineEnd:   li.lineOf(end - 1),
			startByte: d.start,
			endByte:   end,
		})
	}
	return chunks
}

// ---------- 合并与二次切分 ----------

// mergeSmallChunks 把相邻的同类型小块并成一个块，减少噪声。
// 合并时按字节区间重新切片，保证块内容与原文连续。
func mergeSmallChunks(chunks []codeChunk, content string, chunkSize int) []codeChunk {
	out := make([]codeChunk, 0, len(chunks))
	for _, c := range chunks {
		if len(out) == 0 {
			out = append(out, c)
			continue
		}
		prev := &out[len(out)-1]
		if !canMerge(*prev, c, chunkSize) {
			out = append(out, c)
			continue
		}
		mergeInto(prev, c, content)
	}
	return out
}

func canMerge(prev, cur codeChunk, chunkSize int) bool {
	if prev.Kind == kindFileSummary || cur.Kind == kindFileSummary {
		return false
	}
	if prev.Kind != cur.Kind {
		return false
	}
	// 只在"前后两块都很小"时合并：避免把一个小函数并进一个几百行的大函数里，
	// 那会让块的 symbol 与实际内容不符。
	if len(prev.Content) >= mergeBelow || len(cur.Content) >= mergeBelow {
		return false
	}
	if prev.startByte >= prev.endByte || cur.startByte < prev.endByte {
		return false
	}
	if chunkSize > 0 && cur.endByte-prev.startByte > chunkSize {
		return false
	}
	return true
}

func mergeInto(prev *codeChunk, cur codeChunk, content string) {
	start, end := prev.startByte, cur.endByte
	if start >= 0 && end <= len(content) && start < end {
		prev.Content = strings.TrimRight(content[start:end], " \t\r\n")
	}
	prev.LineEnd = cur.LineEnd
	prev.endByte = end
	if cur.Symbol == "" {
		return
	}
	if prev.Symbol == "" {
		prev.Symbol = cur.Symbol
		return
	}
	if !strings.Contains(prev.Symbol, cur.Symbol) {
		prev.Symbol = prev.Symbol + ", " + cur.Symbol
	}
}

// splitOversizedChunks 对超长符号做行窗二次切分，overlap 用于保留跨块的上下文。
// 子块继承原符号名，并把原符号记为 parent，便于回填父块。
func splitOversizedChunks(chunks []codeChunk, chunkSize, chunkOverlap int) []codeChunk {
	if chunkSize <= 0 {
		return chunks
	}
	out := make([]codeChunk, 0, len(chunks))
	for _, c := range chunks {
		if c.Kind == kindFileSummary || len(c.Content) <= chunkSize {
			out = append(out, c)
			continue
		}
		out = append(out, splitChunkByLines(c, chunkSize, chunkOverlap)...)
	}
	return out
}

func splitChunkByLines(c codeChunk, chunkSize, overlap int) []codeChunk {
	lines := strings.Split(c.Content, "\n")
	if len(lines) <= 1 {
		return []codeChunk{c}
	}
	var parts []codeChunk
	for start := 0; start < len(lines); {
		end, size := start, 0
		for end < len(lines) {
			add := len(lines[end]) + 1
			if size > 0 && size+add > chunkSize {
				break
			}
			size += add
			end++
		}
		if end == start {
			end = start + 1
		}
		if text := strings.TrimRight(strings.Join(lines[start:end], "\n"), " \t\r\n"); strings.TrimSpace(text) != "" {
			parts = append(parts, codeChunk{
				Content:   text,
				Symbol:    c.Symbol,
				Kind:      c.Kind,
				Parent:    c.Symbol,
				LineStart: c.LineStart + start,
				LineEnd:   c.LineStart + end - 1,
			})
		}
		if end >= len(lines) {
			break
		}
		next := end - overlapLines(lines, start, end, overlap)
		if next <= start {
			next = end
		}
		start = next
	}
	if len(parts) <= 1 {
		return []codeChunk{c}
	}
	return parts
}

func overlapLines(lines []string, start, end, overlap int) int {
	if overlap <= 0 {
		return 0
	}
	acc, back := 0, 0
	for i := end - 1; i > start; i-- {
		acc += len(lines[i]) + 1
		back++
		if acc >= overlap {
			break
		}
	}
	return back
}

// ---------- 行号索引 ----------

type lineIndex struct {
	starts []int
	size   int
}

func newLineIndex(s string) *lineIndex {
	starts := make([]int, 1, 64)
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return &lineIndex{starts: starts, size: len(s)}
}

// lineOf 返回 offset 所在的 1-based 行号。
func (li *lineIndex) lineOf(offset int) int {
	if offset < 0 {
		return 1
	}
	lo, hi := 0, len(li.starts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if li.starts[mid] <= offset {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1
}

func (li *lineIndex) offsetOfLineStart(offset int) int {
	if offset <= 0 {
		return 0
	}
	return li.starts[li.lineOf(offset)-1]
}

// offsetOfLineEnd 返回 offset 所在行的行尾偏移（不含换行符本身）；
// 用于把块的结束位置扩到整行，避免块尾落在行中间。
func (li *lineIndex) offsetOfLineEnd(offset int) int {
	line := li.lineOf(offset)
	if line >= len(li.starts) {
		return li.size
	}
	return li.starts[line] - 1
}
