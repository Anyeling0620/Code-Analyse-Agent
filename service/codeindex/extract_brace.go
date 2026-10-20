package codeindex

import (
	"bytes"
	"regexp"
	"strings"
)

// braceRule 描述一条声明识别规则。
type braceRule struct {
	re        *regexp.Regexp
	nameAt    int
	kind      string
	kindFn    func(g []string) string
	emit      bool
	scopeType bool // 进入类型作用域（成员可被抽取）
	scopeNS   bool // 命名空间/模块作用域（内部仍视为顶层）
	reject    func(line []byte, g []string) bool
	onMatch   func(f *Facts, g []string, line int)
}

type importRule struct {
	re     *regexp.Regexp
	group  int
	expand func(string) []string
}

type braceSpec struct {
	mask    maskConfig
	decls   []braceRule
	members []braceRule
	imports []importRule
}

var braceSpecs = map[string]*braceSpec{}

func init() {
	braceSpecs[langJavaScript] = jsBraceSpec()
	braceSpecs[langTypeScript] = jsBraceSpec()
	braceSpecs[langJava] = javaBraceSpec()
	braceSpecs[langKotlin] = kotlinBraceSpec()
	braceSpecs[langScala] = scalaBraceSpec()
	braceSpecs[langCSharp] = csharpBraceSpec()
	braceSpecs[langC] = cBraceSpec()
	braceSpecs[langCPP] = cBraceSpec()
	braceSpecs[langRust] = rustBraceSpec()
	braceSpecs[langSwift] = swiftBraceSpec()
	braceSpecs[langDart] = dartBraceSpec()
	braceSpecs[langPHP] = phpBraceSpec()
}

// extractBrace 用「大括号拓扑 + 声明正则」解析大括号语言。
func extractBrace(lang string, spec *braceSpec, src []byte) (Facts, bool) {
	f := Facts{Language: lang}
	code := maskSource(src, spec.mask)
	plain := maskSource(src, keepStrings(spec.mask))
	lines := splitSourceLines(src)
	if len(lines) == 0 {
		return f, true
	}

	type scope struct {
		isType bool
		ns     bool
		name   string
		symIdx int
	}
	type pendingDecl struct {
		name   string
		symIdx int
		isType bool
		isNS   bool
		start  int
	}

	var stack []scope
	var pending *pendingDecl
	prevContent := 0

	topCtx := func() bool {
		for _, s := range stack {
			if !s.ns {
				return false
			}
		}
		return true
	}
	memberCtx := func() bool {
		if len(stack) == 0 {
			return false
		}
		top := stack[len(stack)-1]
		return top.isType && !top.ns
	}

	for _, ln := range lines {
		lineCode := code[ln.start:ln.end]
		if len(bytes.TrimSpace(lineCode)) == 0 {
			continue
		}

		if pending != nil && !headerContinues(code, pending.start, ln.start, lineCode) {
			if pending.symIdx >= 0 {
				f.Symbols[pending.symIdx].LineEnd = prevContent
			}
			pending = nil
		}

		// import 只在顶层（命名空间内仍算顶层）收集。
		if topCtx() {
			linePlain := string(plain[ln.start:ln.end])
			for _, ir := range spec.imports {
				for _, m := range ir.re.FindAllStringSubmatch(linePlain, -1) {
					if ir.group >= len(m) {
						continue
					}
					targets := []string{m[ir.group]}
					if ir.expand != nil {
						targets = ir.expand(m[ir.group])
					}
					for _, t := range targets {
						addEdge(&f, "", t, EdgeImport, ConfidenceExact, ln.num)
					}
				}
			}
		}

		if pending == nil {
			var rule *braceRule
			var g []string
			if topCtx() {
				rule, g = firstRule(spec.decls, lineCode)
			} else if memberCtx() {
				rule, g = firstRule(spec.members, lineCode)
			}
			if rule != nil {
				name := ""
				if rule.nameAt > 0 && rule.nameAt < len(g) {
					name = strings.TrimSpace(g[rule.nameAt])
				}
				parent := ""
				if p, base := splitQualified(name); p != "" {
					parent = p
					name = base
				}
				name = cleanTypeName(name)
				if name == "" && rule.scopeNS {
					name = "(anonymous)"
				}
				if name == "" {
					rule = nil
				} else {
					kind := rule.kind
					if rule.kindFn != nil {
						kind = rule.kindFn(g)
					}
					if parent == "" && memberCtx() && len(stack) > 0 {
						parent = stack[len(stack)-1].name
					}
					headerEnd := findHeaderEnd(plain, ln.start)
					sig := flattenSignature(sliceText(plain, ln.start, headerEnd))
					symIdx := -1
					if rule.emit {
						f.Symbols = append(f.Symbols, Symbol{
							Name:      name,
							Kind:      kind,
							Parent:    parent,
							Signature: sig,
							LineStart: ln.num,
							LineEnd:   ln.num,
						})
						symIdx = len(f.Symbols) - 1
						if rule.scopeType {
							f.Edges = append(f.Edges, typeRelations(lang, sig, name, ln.num)...)
						}
					}
					if rule.onMatch != nil {
						rule.onMatch(&f, g, ln.num)
					}
					pending = &pendingDecl{
						name:   name,
						symIdx: symIdx,
						isType: rule.scopeType,
						isNS:   rule.scopeNS,
						start:  ln.start,
					}
				}
			}
		}

		for i := 0; i < len(lineCode); i++ {
			switch lineCode[i] {
			case '{':
				sc := scope{symIdx: -1}
				if pending != nil {
					sc = scope{
						isType: pending.isType,
						ns:     pending.isNS,
						name:   pending.name,
						symIdx: pending.symIdx,
					}
					pending = nil
				}
				stack = append(stack, sc)
			case '}':
				if n := len(stack); n > 0 {
					top := stack[n-1]
					stack = stack[:n-1]
					if top.symIdx >= 0 {
						f.Symbols[top.symIdx].LineEnd = ln.num
					}
				}
			case ';':
				if pending != nil {
					if pending.symIdx >= 0 {
						f.Symbols[pending.symIdx].LineEnd = ln.num
					}
					pending = nil
				}
			}
		}
		prevContent = ln.num
	}
	if pending != nil && pending.symIdx >= 0 {
		f.Symbols[pending.symIdx].LineEnd = prevContent
	}
	f.Edges = dedupeEdges(f.Edges)
	return f, true
}

func firstRule(rules []braceRule, line []byte) (*braceRule, []string) {
	for i := range rules {
		m := rules[i].re.FindSubmatch(line)
		if m == nil {
			continue
		}
		g := make([]string, len(m))
		for j := range m {
			g[j] = string(m[j])
		}
		if rules[i].reject != nil && rules[i].reject(line, g) {
			continue
		}
		return &rules[i], g
	}
	return nil, nil
}

// headerContinues 判断上一条声明是否还会在本行继续（跨行参数列表或下一行以 { 开头）。
func headerContinues(code []byte, start, lineStart int, lineCode []byte) bool {
	depth := 0
	for i := start; i < lineStart && i < len(code); i++ {
		switch code[i] {
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		}
	}
	if depth > 0 {
		return true
	}
	trimmed := bytes.TrimSpace(lineCode)
	if len(trimmed) == 0 {
		return true
	}
	switch trimmed[0] {
	case '{', ':':
		return true
	}
	return bytes.HasPrefix(trimmed, []byte("where "))
}

// findHeaderEnd 找到声明头结束位置（括号深度为 0 的 { 或 ;）。
func findHeaderEnd(src []byte, start int) int {
	depth := 0
	newlines := 0
	for i := start; i < len(src) && i-start < 4096; i++ {
		switch src[i] {
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case '{', ';':
			if depth == 0 {
				return i
			}
		case '\n':
			if depth == 0 {
				newlines++
				if newlines > 3 {
					return i
				}
			}
		}
	}
	end := start + 4096
	if end > len(src) {
		end = len(src)
	}
	return end
}

func splitQualified(name string) (string, string) {
	if idx := strings.LastIndex(name, "::"); idx > 0 {
		return name[:idx], name[idx+2:]
	}
	if idx := strings.LastIndex(name, "."); idx > 0 {
		return name[:idx], name[idx+1:]
	}
	return "", name
}

func cleanTypeName(name string) string {
	name = strings.TrimSpace(name)
	if idx := strings.IndexByte(name, '<'); idx > 0 {
		name = strings.TrimSpace(name[:idx])
	}
	if idx := strings.LastIndex(name, "::"); idx > 0 {
		name = name[idx+2:]
	}
	name = strings.TrimPrefix(name, "~")
	return name
}

const jsIdent = `[A-Za-z_$][\w$]*`

var jsMemberKeywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true,
	"return": true, "function": true, "new": true, "typeof": true, "do": true,
	"else": true, "with": true, "super": true, "this": true,
}

func jsVarKind(g []string) string {
	if len(g) > 1 && g[1] == "const" {
		return KindConst
	}
	return KindVar
}

func jsMemberMethodReject(line []byte, g []string) bool {
	if len(g) < 2 || jsMemberKeywords[g[1]] {
		return true
	}
	return !bytes.ContainsAny(line, "{;")
}

func jsBraceSpec() *braceSpec {
	return &braceSpec{
		mask: maskConfig{
			lineComments:  []string{"//"},
			blockComments: [][2]string{{"/*", "*/"}},
			singleQuote:   true,
			doubleQuote:   true,
			backtick:      true,
			regexLiteral:  true,
		},
		decls: []braceRule{
			{re: regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:declare\s+)?(?:abstract\s+)?class\s+(` + jsIdent + `)`), nameAt: 1, kind: KindClass, emit: true, scopeType: true},
			{re: regexp.MustCompile(`^\s*(?:export\s+)?(?:declare\s+)?interface\s+(` + jsIdent + `)`), nameAt: 1, kind: KindInterface, emit: true, scopeType: true},
			{re: regexp.MustCompile(`^\s*(?:export\s+)?(?:declare\s+)?(?:const\s+)?enum\s+(` + jsIdent + `)`), nameAt: 1, kind: KindEnum, emit: true, scopeType: true},
			{re: regexp.MustCompile(`^\s*(?:export\s+)?(?:declare\s+)?type\s+(` + jsIdent + `)\s*(?:<[^>]*>)?\s*=`), nameAt: 1, kind: KindType, emit: true},
			{re: regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:declare\s+)?(?:async\s+)?function\s*\*?\s*(` + jsIdent + `)`), nameAt: 1, kind: KindFunc, emit: true},
			{re: regexp.MustCompile(`^\s*(?:export\s+)?(?:declare\s+)?(const|let|var)\s+(` + jsIdent + `)\s*(?::|=|;|!)`), nameAt: 2, kindFn: jsVarKind, emit: true},
		},
		members: []braceRule{
			{re: regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|readonly|abstract|async|override|declare|accessor)\s+)*(?:\*\s*)?(` + jsIdent + `)\s*[?!]?\s*(?:<[^>]*>)?\s*\(`), nameAt: 1, kind: KindMethod, emit: true, reject: jsMemberMethodReject},
			{re: regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|readonly|declare|abstract|override)\s+)*(#?` + jsIdent + `)\s*[?!]?\s*:`), nameAt: 1, kind: KindVar, emit: true},
			{re: regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|readonly|declare|abstract|override)\s+)*(#?` + jsIdent + `)\s*[?!]?\s*=`), nameAt: 1, kind: KindVar, emit: true},
		},
		imports: []importRule{
			{re: regexp.MustCompile(`^\s*import\s+(?:[\w*{}$,\s]+\s+from\s+)?['"]([^'"]+)['"]`), group: 1},
			{re: regexp.MustCompile(`^\s*export\s+(?:\*|\{[^}]*\})\s+from\s+['"]([^'"]+)['"]`), group: 1},
			{re: regexp.MustCompile(`\brequire\s*\(\s*['"]([^'"]+)['"]\s*\)`), group: 1},
			{re: regexp.MustCompile(`^\s*import\s+['"]([^'"]+)['"]`), group: 1},
		},
	}
}

func javaTypeKind(g []string) string {
	switch g[1] {
	case "interface", "@interface":
		return KindInterface
	case "enum":
		return KindEnum
	default:
		return KindClass
	}
}

func csTypeKind(g []string) string {
	switch g[1] {
	case "interface":
		return KindInterface
	case "struct":
		return KindStruct
	case "enum":
		return KindEnum
	default:
		return KindClass
	}
}

func kotlinTypeKind(g []string) string {
	if g[1] == "interface" {
		return KindInterface
	}
	return KindClass
}

func scalaTypeKind(g []string) string {
	switch g[1] {
	case "trait":
		return KindTrait
	case "object":
		return KindModule
	case "enum":
		return KindEnum
	default:
		return KindClass
	}
}

func cTypeKind(g []string) string {
	switch {
	case strings.HasPrefix(g[1], "enum"):
		return KindEnum
	case g[1] == "class":
		return KindClass
	default:
		return KindStruct
	}
}

func rustTypeKind(g []string) string {
	switch g[1] {
	case "struct", "union":
		return KindStruct
	case "trait":
		return KindTrait
	default:
		return KindType
	}
}

func swiftTypeKind(g []string) string {
	switch g[1] {
	case "protocol":
		return KindInterface
	case "struct":
		return KindStruct
	case "enum":
		return KindEnum
	case "extension", "typealias":
		return KindType
	default:
		return KindClass
	}
}

func dartTypeKind(g []string) string {
	switch g[1] {
	case "mixin":
		return KindTrait
	case "enum":
		return KindEnum
	case "extension":
		return KindType
	default:
		return KindClass
	}
}

func phpTypeKind(g []string) string {
	switch g[1] {
	case "interface":
		return KindInterface
	case "trait":
		return KindTrait
	case "enum":
		return KindEnum
	default:
		return KindClass
	}
}

func valKind(g []string) string {
	if len(g) > 1 && g[1] == "const" {
		return KindConst
	}
	return KindVar
}

func letKind(g []string) string {
	if len(g) > 1 && g[1] == "let" {
		return KindConst
	}
	return KindVar
}

// methodNotKeyword 排除把 if/for 等误认成方法名。
func methodNotKeyword(line []byte, g []string) bool {
	for _, v := range g[1:] {
		switch v {
		case "if", "for", "while", "switch", "catch", "return", "new", "do", "else", "sizeof", "foreach", "using", "lock", "fixed":
			return true
		}
	}
	return false
}

// cFuncReject 要求 C/C++ 函数声明必须在同一行有函数体。
func cFuncReject(line []byte, g []string) bool {
	if len(g) <= 2 {
		return true
	}
	switch g[2] {
	case "if", "for", "while", "switch", "catch", "return", "sizeof", "else", "do":
		return true
	}
	open := bytes.IndexByte(line, '{')
	if open < 0 {
		return true
	}
	if semi := bytes.IndexByte(line, ';'); semi >= 0 && semi < open {
		return true
	}
	return false
}

func cTypeReject(line []byte, g []string) bool {
	return bytes.Contains([]byte(g[0]), []byte("("))
}

func javaBraceSpec() *braceSpec {
	return &braceSpec{
		mask: maskConfig{
			lineComments:  []string{"//"},
			blockComments: [][2]string{{"/*", "*/"}},
			singleQuote:   true,
			doubleQuote:   true,
		},
		decls: []braceRule{
			{re: regexp.MustCompile(`^\s*(?:@\w+(?:\s*\([^)]*\))?\s*)*(?:(?:public|protected|private|abstract|final|sealed|non-sealed|static|strictfp)\s+)*(class|interface|enum|record|@interface)\s+(\w+)`), nameAt: 2, kindFn: javaTypeKind, emit: true, scopeType: true},
		},
		members: []braceRule{
			{re: regexp.MustCompile(`^\s*(?:@\w+(?:\s*\([^)]*\))?\s*)*(?:(?:public|protected|private|static|final|abstract|synchronized|native|default|strictfp)\s+)*(?:<[^>]+>\s*)?([\w$.\[\]]+(?:<[^;{()]*>)?(?:\[\])*)\s+(\w+)\s*\(`), nameAt: 2, kind: KindMethod, emit: true, reject: methodNotKeyword},
			{re: regexp.MustCompile(`^\s*(?:@\w+(?:\s*\([^)]*\))?\s*)*(?:public|protected|private)\s+(\w+)\s*\(`), nameAt: 1, kind: KindMethod, emit: true},
		},
		imports: []importRule{
			{re: regexp.MustCompile(`^\s*import\s+(?:static\s+)?([\w.*]+)\s*;`), group: 1, expand: trimStarImport},
		},
	}
}

func kotlinBraceSpec() *braceSpec {
	funRe := `^\s*(?:(?:private|public|protected|internal|open|override|abstract|final|inline|suspend|tailrec|operator|infix|external|expect|actual)\s+)*fun\s+(?:<[^>]+>\s*)?(?:([\w.<>?]+)\.)?(\w+)\s*\(`
	valRe := `^\s*(?:(?:private|public|protected|internal|const|override|lateinit|open|expect|actual)\s+)*(val|var)\s+(\w+)`
	return &braceSpec{
		mask: maskConfig{
			lineComments:  []string{"//"},
			blockComments: [][2]string{{"/*", "*/"}},
			singleQuote:   true,
			doubleQuote:   true,
			tripleQuote:   true,
		},
		decls: []braceRule{
			{re: regexp.MustCompile(`^\s*(?:@\w+(?:\s*\([^)]*\))?\s*)*(?:(?:data|sealed|abstract|open|enum|annotation|value|inner|private|public|protected|internal|expect|actual|inline|companion)\s+)*(class|interface|object)\s+(\w+)`), nameAt: 2, kindFn: kotlinTypeKind, emit: true, scopeType: true},
			{re: regexp.MustCompile(funRe), nameAt: 2, kind: KindFunc, emit: true},
			{re: regexp.MustCompile(valRe), nameAt: 2, kindFn: valKind, emit: true},
		},
		members: []braceRule{
			{re: regexp.MustCompile(funRe), nameAt: 2, kind: KindMethod, emit: true},
			{re: regexp.MustCompile(valRe), nameAt: 2, kindFn: valKind, emit: true},
		},
		imports: []importRule{
			{re: regexp.MustCompile(`^\s*import\s+([\w.*]+)`), group: 1, expand: trimStarImport},
		},
	}
}

func scalaBraceSpec() *braceSpec {
	defRe := `^\s*(?:(?:private|protected|override|final|implicit|inline|transparent|lazy)\s+)*def\s+([\w+\-*/=<>!&|^%:]+)`
	return &braceSpec{
		mask: maskConfig{
			lineComments:  []string{"//"},
			blockComments: [][2]string{{"/*", "*/"}},
			singleQuote:   true,
			doubleQuote:   true,
			tripleQuote:   true,
		},
		decls: []braceRule{
			{re: regexp.MustCompile(`^\s*(?:(?:abstract|sealed|final|implicit|private|protected|case|override|lazy|open)\s+)*(class|trait|object|enum)\s+(\w+)`), nameAt: 2, kindFn: scalaTypeKind, emit: true, scopeType: true},
			{re: regexp.MustCompile(defRe), nameAt: 1, kind: KindFunc, emit: true},
			{re: regexp.MustCompile(`^\s*(?:(?:private|protected|override|final|implicit|lazy)\s+)*(val|var)\s+(\w+)`), nameAt: 2, kindFn: valKind, emit: true},
		},
		members: []braceRule{
			{re: regexp.MustCompile(defRe), nameAt: 1, kind: KindMethod, emit: true},
			{re: regexp.MustCompile(`^\s*(?:(?:private|protected|override|final|implicit|lazy)\s+)*(val|var)\s+(\w+)`), nameAt: 2, kindFn: valKind, emit: true},
		},
		imports: []importRule{
			{re: regexp.MustCompile(`^\s*import\s+([\w.]+)`), group: 1},
		},
	}
}

func csharpBraceSpec() *braceSpec {
	return &braceSpec{
		mask: maskConfig{
			lineComments:  []string{"//"},
			blockComments: [][2]string{{"/*", "*/"}},
			singleQuote:   true,
			doubleQuote:   true,
		},
		decls: []braceRule{
			{re: regexp.MustCompile(`^\s*(?:\[[^\]]*\]\s*)*(?:(?:public|private|protected|internal|static|sealed|abstract|partial|readonly|unsafe|new|file)\s+)*(class|interface|struct|record|enum)\s+(\w+)`), nameAt: 2, kindFn: csTypeKind, emit: true, scopeType: true},
			{re: regexp.MustCompile(`^\s*namespace\s+([\w.]+)`), nameAt: 1, emit: false, scopeNS: true},
		},
		members: []braceRule{
			{re: regexp.MustCompile(`^\s*(?:\[[^\]]*\]\s*)*(?:(?:public|private|protected|internal|static|virtual|override|abstract|async|sealed|extern|unsafe|new|partial|readonly)\s+)*(?:[\w.<>\[\],?]+\s+)+(\w+)\s*\(`), nameAt: 1, kind: KindMethod, emit: true, reject: methodNotKeyword},
			{re: regexp.MustCompile(`^\s*(?:\[[^\]]*\]\s*)*(?:(?:public|private|protected|internal|static|virtual|override|abstract|required|readonly)\s+)+([\w.<>\[\],?]+)\s+(\w+)\s*\{`), nameAt: 2, kind: KindVar, emit: true},
		},
		imports: []importRule{
			{re: regexp.MustCompile(`^\s*using\s+(?:static\s+)?([\w.]+)\s*;`), group: 1},
		},
	}
}

func cBraceSpec() *braceSpec {
	funcRe := `^\s*(?:(?:static|inline|virtual|explicit|constexpr|consteval|constinit|extern|friend|const|volatile|unsigned|signed|long|short|struct|typename)\s+)*([A-Za-z_][\w:<>,*&\s]*?)\s+((?:[A-Za-z_]\w*::)*~?[A-Za-z_]\w*)\s*\(`
	return &braceSpec{
		mask: maskConfig{
			lineComments:  []string{"//"},
			blockComments: [][2]string{{"/*", "*/"}},
			singleQuote:   true,
			doubleQuote:   true,
		},
		decls: []braceRule{
			{re: regexp.MustCompile(`^\s*(?:inline\s+)?namespace(?:\s+(\w+))?\s*\{`), nameAt: 1, emit: false, scopeNS: true},
			{re: regexp.MustCompile(`^\s*(?:typedef\s+)?(?:template\s*<[^>]*>\s*)?(class|struct|union|enum(?:\s+class)?)\s+(\w+)[^;{]*`), nameAt: 2, kindFn: cTypeKind, emit: true, scopeType: true, reject: cTypeReject},
			{re: regexp.MustCompile(funcRe), nameAt: 2, kind: KindFunc, emit: true, reject: cFuncReject},
		},
		members: []braceRule{
			{re: regexp.MustCompile(funcRe), nameAt: 2, kind: KindMethod, emit: true, reject: cFuncReject},
		},
		imports: []importRule{
			{re: regexp.MustCompile(`^\s*#\s*include\s*[<"]([^>"]+)[>"]`), group: 1},
		},
	}
}

func rustBraceSpec() *braceSpec {
	fnRe := `^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+|unsafe\s+|const\s+|extern\s+"[^"]*"\s+)*fn\s+(\w+)`
	return &braceSpec{
		mask: maskConfig{
			lineComments:  []string{"//"},
			blockComments: [][2]string{{"/*", "*/"}},
			doubleQuote:   true,
			rustLifetime:  true,
		},
		decls: []braceRule{
			{re: regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?mod\s+(\w+)`), nameAt: 1, kind: KindModule, emit: true, scopeNS: true},
			{re: regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(struct|enum|trait|union|type)\s+(\w+)`), nameAt: 2, kindFn: rustTypeKind, emit: true, scopeType: true},
			{re: regexp.MustCompile(`^\s*(?:unsafe\s+)?impl(?:<[^>]*>)?\s+(?:([\w:<>,\s]+?)\s+for\s+)?([\w:<>]+)`), nameAt: 2, emit: false, scopeType: true, onMatch: rustImplEdges},
			{re: regexp.MustCompile(fnRe), nameAt: 1, kind: KindFunc, emit: true},
			{re: regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(const|static)\s+(?:mut\s+)?(\w+)`), nameAt: 2, kind: KindConst, emit: true},
		},
		members: []braceRule{
			{re: regexp.MustCompile(fnRe), nameAt: 1, kind: KindMethod, emit: true},
		},
		imports: []importRule{
			{re: regexp.MustCompile(`^\s*(?:pub\s+)?use\s+([^;]+);`), group: 1, expand: rustUsePaths},
		},
	}
}

func rustImplEdges(f *Facts, g []string, line int) {
	if len(g) < 3 {
		return
	}
	trait := strings.TrimSpace(g[1])
	if trait == "" {
		return
	}
	addEdge(f, cleanTypeName(g[2]), trait, EdgeImplements, ConfidenceExact, line)
}

func rustUsePaths(s string) []string {
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, " as "); idx > 0 {
		s = strings.TrimSpace(s[:idx])
	}
	s = strings.TrimSuffix(s, "::*")
	if idx := strings.Index(s, "{"); idx >= 0 {
		prefix := strings.TrimSuffix(strings.TrimSpace(s[:idx]), "::")
		inner := s[idx+1:]
		if j := strings.Index(inner, "}"); j >= 0 {
			inner = inner[:j]
		}
		first := strings.TrimSpace(strings.Split(inner, ",")[0])
		first = strings.TrimPrefix(first, "self::")
		if first == "" || first == "self" {
			if prefix != "" {
				return []string{prefix}
			}
			return nil
		}
		if prefix == "" {
			return []string{first}
		}
		return []string{prefix + "::" + first}
	}
	if s == "self" || s == "" {
		return nil
	}
	return []string{s}
}

func swiftTypeReject(line []byte, g []string) bool {
	if len(g) < 3 {
		return true
	}
	switch g[2] {
	case "func", "var", "let", "subscript", "init", "deinit":
		return true
	}
	return false
}

func swiftBraceSpec() *braceSpec {
	mods := `(?:(?:public|private|fileprivate|internal|open|final|static|override|mutating|nonmutating|indirect|required|convenience|lazy|weak|unowned)\s+)*`
	return &braceSpec{
		mask: maskConfig{
			lineComments:  []string{"//"},
			blockComments: [][2]string{{"/*", "*/"}},
			doubleQuote:   true,
			tripleQuote:   true,
		},
		decls: []braceRule{
			{re: regexp.MustCompile(`^\s*(?:@\w+(?:\s*\([^)]*\))?\s*)*` + mods + `(class|struct|enum|protocol|extension|typealias)\s+(\w+)`), nameAt: 2, kindFn: swiftTypeKind, emit: true, scopeType: true, reject: swiftTypeReject},
			{re: regexp.MustCompile(`^\s*(?:@\w+(?:\s*\([^)]*\))?\s*)*` + mods + `func\s+(\w+)`), nameAt: 1, kind: KindFunc, emit: true},
			{re: regexp.MustCompile(`^\s*(?:@\w+\s+)*` + mods + `(let|var)\s+(\w+)`), nameAt: 2, kindFn: letKind, emit: true},
		},
		members: []braceRule{
			{re: regexp.MustCompile(`^\s*(?:@\w+(?:\s*\([^)]*\))?\s*)*` + mods + `func\s+(\w+)`), nameAt: 1, kind: KindMethod, emit: true},
			{re: regexp.MustCompile(`^\s*(?:@\w+\s+)*` + mods + `(let|var)\s+(\w+)`), nameAt: 2, kindFn: letKind, emit: true},
		},
		imports: []importRule{
			{re: regexp.MustCompile(`^\s*(?:@\w+\s+)?import\s+(\w+)`), group: 1},
		},
	}
}

func dartBraceSpec() *braceSpec {
	return &braceSpec{
		mask: maskConfig{
			lineComments:  []string{"//"},
			blockComments: [][2]string{{"/*", "*/"}},
			singleQuote:   true,
			doubleQuote:   true,
			tripleQuote:   true,
		},
		decls: []braceRule{
			{re: regexp.MustCompile(`^\s*(?:(?:abstract|sealed|base|final|interface|mixin|external)\s+)*(class|mixin|enum|extension)\s+(\w+)`), nameAt: 2, kindFn: dartTypeKind, emit: true, scopeType: true},
			{re: regexp.MustCompile(`^\s*(?:@\w+\s*)*(?:(?:static|final|const|abstract|external|factory|late|override|required|covariant)\s+)*([\w<>\[\],?]+\s+)(\w+)\s*\(`), nameAt: 2, kind: KindFunc, emit: true, reject: methodNotKeyword},
		},
		members: []braceRule{
			{re: regexp.MustCompile(`^\s*(?:@\w+\s*)*(?:(?:static|final|const|abstract|external|factory|late|override|required|covariant)\s+)*([\w<>\[\],?]+\s+)(\w+)\s*\(`), nameAt: 2, kind: KindMethod, emit: true, reject: methodNotKeyword},
		},
		imports: []importRule{
			{re: regexp.MustCompile(`^\s*import\s+['"]([^'"]+)['"]`), group: 1},
		},
	}
}

func phpBraceSpec() *braceSpec {
	funcRe := `^\s*(?:(?:public|private|protected|static|final|abstract)\s+)*function\s+&?\s*(\w+)\s*\(`
	return &braceSpec{
		mask: maskConfig{
			lineComments:  []string{"//", "#"},
			blockComments: [][2]string{{"/*", "*/"}},
			singleQuote:   true,
			doubleQuote:   true,
		},
		decls: []braceRule{
			{re: regexp.MustCompile(`^\s*(?:(?:abstract|final|readonly)\s+)*(class|interface|trait|enum)\s+(\w+)`), nameAt: 2, kindFn: phpTypeKind, emit: true, scopeType: true},
			{re: regexp.MustCompile(`^\s*namespace\s+([\w\\]+)\s*[;{]`), nameAt: 1, emit: false, scopeNS: true},
			{re: regexp.MustCompile(funcRe), nameAt: 1, kind: KindFunc, emit: true, reject: methodNotKeyword},
		},
		members: []braceRule{
			{re: regexp.MustCompile(funcRe), nameAt: 1, kind: KindMethod, emit: true, reject: methodNotKeyword},
		},
		imports: []importRule{
			{re: regexp.MustCompile(`^\s*use\s+([\w\\]+)`), group: 1},
			{re: regexp.MustCompile(`^\s*(?:require|require_once|include|include_once)\s*\(?\s*['"]([^'"]+)['"]`), group: 1},
		},
	}
}

func trimStarImport(s string) []string {
	s = strings.TrimSuffix(strings.TrimSpace(s), ".*")
	s = strings.TrimSuffix(s, ".")
	if s == "" {
		return nil
	}
	return []string{s}
}

var (
	reExtendsClause    = regexp.MustCompile(`\bextends\s+([^{]+?)(?:\bimplements\b|\bwith\b|\bwhere\b|\{|$)`)
	reImplementsClause = regexp.MustCompile(`\bimplements\s+([^{]+?)(?:\{|$)`)
	reWithClause       = regexp.MustCompile(`\bwith\s+([^{]+?)(?:\{|$)`)
	reColonClause      = regexp.MustCompile(`(?:class|struct|interface|trait|object|record|enum|protocol|extension)\s+[\w<>.$]+\s*(?:\([^{)]*\))?\s*:\s*([^{]+?)(?:\bwhere\b|\{|$)`)
)

// typeRelations 从声明头解析 extends/implements 关系。
// 显式关键字的置信度为 exact；C++/C#/Kotlin/Swift 的 ":" 列表无法区分继承与实现，标记 heuristic。
func typeRelations(lang, header, name string, line int) []Edge {
	if header == "" || name == "" {
		return nil
	}
	var out []Edge
	if m := reExtendsClause.FindStringSubmatch(header); m != nil {
		for _, base := range splitTypeList(m[1]) {
			out = append(out, Edge{FromSymbol: name, ToRaw: base, Kind: EdgeExtends, Confidence: ConfidenceExact, Line: line})
		}
	}
	if m := reImplementsClause.FindStringSubmatch(header); m != nil {
		for _, base := range splitTypeList(m[1]) {
			out = append(out, Edge{FromSymbol: name, ToRaw: base, Kind: EdgeImplements, Confidence: ConfidenceExact, Line: line})
		}
	}
	if m := reColonClause.FindStringSubmatch(header); m != nil {
		for _, base := range splitTypeList(m[1]) {
			out = append(out, Edge{FromSymbol: name, ToRaw: base, Kind: EdgeExtends, Confidence: ConfidenceHeuristic, Line: line})
		}
	}
	if m := reWithClause.FindStringSubmatch(header); m != nil {
		for _, base := range splitTypeList(m[1]) {
			out = append(out, Edge{FromSymbol: name, ToRaw: base, Kind: EdgeExtends, Confidence: ConfidenceHeuristic, Line: line})
		}
	}
	return dedupeEdges(out)
}

// splitTypeList 按逗号切分基类列表，忽略泛型/参数里的逗号。
func splitTypeList(s string) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<', '(', '[':
			depth++
		case '>', ')', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, cleanBaseName(s[start:i]))
				start = i + 1
			}
		}
	}
	out = append(out, cleanBaseName(s[start:]))
	filtered := out[:0]
	for _, item := range out {
		if item != "" {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func cleanBaseName(s string) string {
	s = strings.TrimSpace(s)
	for _, kw := range []string{"public", "private", "protected", "internal", "virtual", "open", "final", "abstract", "sealed", "partial"} {
		if strings.HasPrefix(s, kw+" ") {
			s = strings.TrimSpace(s[len(kw):])
		}
	}
	s = strings.TrimSuffix(s, "()")
	if idx := strings.IndexByte(s, '<'); idx > 0 {
		s = strings.TrimSpace(s[:idx])
	}
	s = strings.TrimRight(s, "?!*& ")
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "=(){};") {
		return ""
	}
	if s == "object" {
		return ""
	}
	return s
}

func dedupeEdges(in []Edge) []Edge {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, e := range in {
		key := e.FromSymbol + "|" + e.Kind + "|" + e.ToRaw
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}
