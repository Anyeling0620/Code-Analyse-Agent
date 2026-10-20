package codeindex

import (
	"regexp"
	"strings"
)

var (
	rePyClass  = regexp.MustCompile(`^class\s+([A-Za-z_]\w*)\s*(?:\(([^)]*)\))?\s*:`)
	rePyDef    = regexp.MustCompile(`^(?:async\s+)?def\s+([A-Za-z_]\w*)\s*\(`)
	rePyImport = regexp.MustCompile(`^import\s+(.+)$`)
	rePyFrom   = regexp.MustCompile(`^from\s+([\w.]+)\s+import\b`)
	rePyAssign = regexp.MustCompile(`^([A-Za-z_]\w*)\s*(?::.*?)?=(.*)$`)
)

// pyScope 表示当前缩进块。kind 取 KindClass / KindFunc。
type pyScope struct {
	indent int
	name   string
	kind   string
	symIdx int
}

// extractPython 按缩进解析 Python：类、方法、函数、模块级赋值与 import。
func extractPython(lang string, src []byte) (Facts, bool) {
	f := Facts{Language: lang}
	cfg := maskConfig{
		lineComments: []string{"#"},
		singleQuote:  true,
		doubleQuote:  true,
		tripleQuote:  true,
	}
	code := maskSource(src, cfg)
	plain := maskSource(src, keepStrings(cfg))
	lines := splitSourceLines(src)
	if len(lines) == 0 {
		return f, true
	}

	stack := []pyScope{{indent: -1}}
	lastContent := 0
	var decorators []string

	for _, ln := range lines {
		masked := code[ln.start:ln.end]
		trimmed := strings.TrimSpace(string(masked))
		if trimmed == "" {
			continue
		}
		prevContent := lastContent
		lastContent = ln.num
		indent := pythonIndent(masked)

		for len(stack) > 1 && indent <= stack[len(stack)-1].indent {
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if top.symIdx >= 0 {
				f.Symbols[top.symIdx].LineEnd = prevContent
			}
		}

		if strings.HasPrefix(trimmed, "@") {
			decorators = append(decorators, strings.TrimSpace(string(plain[ln.start:ln.end])))
			continue
		}

		if m := rePyClass.FindStringSubmatch(trimmed); m != nil {
			top := stack[len(stack)-1]
			emit := len(stack) == 1 || top.kind == KindClass
			parent := ""
			if top.kind == KindClass {
				parent = top.name
			}
			symIdx := -1
			if emit {
				end := pyHeaderEnd(code, ln.start)
				sig := flattenSignature(strings.Join(decorators, " ") + " " + sliceText(plain, ln.start, end))
				f.Symbols = append(f.Symbols, Symbol{
					Name:      m[1],
					Kind:      KindClass,
					Parent:    parent,
					Signature: sig,
					LineStart: ln.num,
					LineEnd:   ln.num,
				})
				symIdx = len(f.Symbols) - 1
				for _, base := range pyBases(m[2]) {
					addEdge(&f, m[1], base, EdgeExtends, ConfidenceExact, ln.num)
				}
			}
			stack = append(stack, pyScope{indent: indent, name: m[1], kind: KindClass, symIdx: symIdx})
			decorators = nil
			continue
		}

		if m := rePyDef.FindStringSubmatch(trimmed); m != nil {
			kind := KindFunc
			parent := ""
			emit := true
			if len(stack) > 1 {
				top := stack[len(stack)-1]
				if top.kind == KindClass {
					kind = KindMethod
					parent = top.name
				} else {
					emit = false
				}
			}
			symIdx := -1
			if emit {
				end := pyHeaderEnd(code, ln.start)
				sig := flattenSignature(strings.Join(decorators, " ") + " " + sliceText(plain, ln.start, end))
				f.Symbols = append(f.Symbols, Symbol{
					Name:      m[1],
					Kind:      kind,
					Parent:    parent,
					Signature: sig,
					LineStart: ln.num,
					LineEnd:   ln.num,
				})
				symIdx = len(f.Symbols) - 1
			}
			stack = append(stack, pyScope{indent: indent, name: m[1], kind: KindFunc, symIdx: symIdx})
			decorators = nil
			continue
		}

		decorators = nil

		if indent == 0 {
			if m := rePyImport.FindStringSubmatch(trimmed); m != nil {
				for _, part := range strings.Split(m[1], ",") {
					mod := strings.TrimSpace(part)
					if idx := strings.Index(mod, " as "); idx >= 0 {
						mod = strings.TrimSpace(mod[:idx])
					}
					addEdge(&f, "", mod, EdgeImport, ConfidenceExact, ln.num)
				}
				continue
			}
			if m := rePyFrom.FindStringSubmatch(trimmed); m != nil {
				addEdge(&f, "", m[1], EdgeImport, ConfidenceExact, ln.num)
				continue
			}
			if m := rePyAssign.FindStringSubmatch(trimmed); m != nil && len(stack) == 1 && !strings.HasPrefix(m[2], "=") {
				kind := KindVar
				if isUpperName(m[1]) {
					kind = KindConst
				}
				f.Symbols = append(f.Symbols, Symbol{
					Name:      m[1],
					Kind:      kind,
					Signature: flattenSignature(sliceText(plain, ln.start, ln.end)),
					LineStart: ln.num,
					LineEnd:   ln.num,
				})
			}
		}
	}

	for len(stack) > 1 {
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if top.symIdx >= 0 {
			f.Symbols[top.symIdx].LineEnd = lastContent
		}
	}
	return f, true
}

func pythonIndent(line []byte) int {
	n := 0
	for _, c := range line {
		switch c {
		case ' ':
			n++
		case '\t':
			n += 8 - n%8
		case '\f', '\v', '\r':
			n = 0
		default:
			return n
		}
	}
	return n
}

// pyHeaderEnd 找到声明头的结束冒号（括号嵌套深度为 0），返回其偏移。
func pyHeaderEnd(code []byte, start int) int {
	depth := 0
	for i := start; i < len(code); i++ {
		switch code[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case ':':
			if depth == 0 {
				return i
			}
		case '\n':
			if depth == 0 {
				return i
			}
		}
	}
	return len(code)
}

// pyBases 解析 class 头里的基类列表，跳过 object/metaclass/关键字参数。
func pyBases(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	for _, base := range splitTypeList(s) {
		switch {
		case base == "", base == "object":
			continue
		case strings.Contains(base, "="):
			continue
		}
		out = append(out, base)
	}
	return out
}

func isUpperName(s string) bool {
	hasLetter := false
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			return false
		}
		if r >= 'A' && r <= 'Z' {
			hasLetter = true
		}
	}
	return hasLetter
}
