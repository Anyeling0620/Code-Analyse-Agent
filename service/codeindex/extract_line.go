package codeindex

import (
	"regexp"
	"strings"
)

var (
	reRubyClass   = regexp.MustCompile(`^class\s+([A-Z][\w:]*)\s*(?:<\s*([\w:]+))?`)
	reRubyModule  = regexp.MustCompile(`^module\s+([A-Z][\w:]*)`)
	reRubyDef     = regexp.MustCompile(`^def\s+(?:self\.)?([\w?!=\[\]]+)`)
	reRubyRequire = regexp.MustCompile(`^require(?:_relative)?\s*\(?\s*['"]([^'"]+)['"]`)
	reRubyOpen    = regexp.MustCompile(`^(?:if|unless|while|until|case|begin|for)\b|(?:\s|^)do\b(?:\s*\|[^|]*\|)?\s*$`)
	reRubyEnd     = regexp.MustCompile(`^end\b`)

	reLuaFunc       = regexp.MustCompile(`^(?:local\s+)?function\s+([\w.:]+)\s*\(`)
	reLuaAssignFunc = regexp.MustCompile(`^(?:local\s+)?([\w.:]+)\s*=\s*function\s*\(`)
	reLuaRequire    = regexp.MustCompile(`require\s*\(?\s*['"]([^'"]+)['"]`)
	reLuaOpen       = regexp.MustCompile(`^(?:local\s+)?(?:function\b|if\b|for\b|while\b|repeat\b|do\b)`)
	reLuaClose      = regexp.MustCompile(`^(?:end|until)\b`)
)

type lineFrame struct {
	symIdx int
	name   string
	kind   string
}

// extractRuby 按关键字块（class/module/def ... end）解析 Ruby。
func extractRuby(lang string, src []byte) (Facts, bool) {
	f := Facts{Language: lang}
	cfg := maskConfig{
		lineComments:  []string{"#"},
		blockComments: [][2]string{{"=begin", "=end"}},
		singleQuote:   true,
		doubleQuote:   true,
	}
	code := maskSource(src, cfg)
	plain := maskSource(src, keepStrings(cfg))
	lines := splitSourceLines(src)
	if len(lines) == 0 {
		return f, true
	}

	var stack []lineFrame
	lastContent := 0

	for _, ln := range lines {
		masked := code[ln.start:ln.end]
		trimmed := strings.TrimSpace(string(masked))
		if trimmed == "" {
			continue
		}
		lastContent = ln.num

		if reRubyEnd.MatchString(trimmed) {
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if top.symIdx >= 0 {
					f.Symbols[top.symIdx].LineEnd = ln.num
				}
			}
			continue
		}

		parent := ""
		enclosing := ""
		if len(stack) > 0 {
			parent = stack[len(stack)-1].name
			enclosing = stack[len(stack)-1].kind
		}

		if m := reRubyClass.FindStringSubmatch(trimmed); m != nil {
			sig := flattenSignature(sliceText(plain, ln.start, ln.end))
			f.Symbols = append(f.Symbols, Symbol{
				Name: m[1], Kind: KindClass, Parent: parent,
				Signature: sig, LineStart: ln.num, LineEnd: ln.num,
			})
			if base := strings.TrimSpace(m[2]); base != "" {
				addEdge(&f, m[1], base, EdgeExtends, ConfidenceExact, ln.num)
			}
			stack = append(stack, lineFrame{symIdx: len(f.Symbols) - 1, name: m[1], kind: KindClass})
			continue
		}
		if m := reRubyModule.FindStringSubmatch(trimmed); m != nil {
			sig := flattenSignature(sliceText(plain, ln.start, ln.end))
			f.Symbols = append(f.Symbols, Symbol{
				Name: m[1], Kind: KindModule, Parent: parent,
				Signature: sig, LineStart: ln.num, LineEnd: ln.num,
			})
			stack = append(stack, lineFrame{symIdx: len(f.Symbols) - 1, name: m[1], kind: KindModule})
			continue
		}
		if m := reRubyDef.FindStringSubmatch(trimmed); m != nil {
			kind := KindFunc
			p := ""
			if enclosing == KindClass || enclosing == KindModule {
				kind = KindMethod
				p = parent
			}
			sig := flattenSignature(sliceText(plain, ln.start, ln.end))
			f.Symbols = append(f.Symbols, Symbol{
				Name: m[1], Kind: kind, Parent: p,
				Signature: sig, LineStart: ln.num, LineEnd: ln.num,
			})
			stack = append(stack, lineFrame{symIdx: len(f.Symbols) - 1, name: m[1], kind: kind})
			continue
		}
		if len(stack) == 0 {
			plainLine := strings.TrimSpace(string(plain[ln.start:ln.end]))
			if m := reRubyRequire.FindStringSubmatch(plainLine); m != nil {
				addEdge(&f, "", m[1], EdgeImport, ConfidenceExact, ln.num)
				continue
			}
		}
		if reRubyOpen.MatchString(trimmed) {
			stack = append(stack, lineFrame{symIdx: -1, kind: "block"})
		}
	}

	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].symIdx >= 0 {
			f.Symbols[stack[i].symIdx].LineEnd = lastContent
		}
	}
	return f, true
}

// extractLua 按 function/if/for/... end 结构解析 Lua。
func extractLua(lang string, src []byte) (Facts, bool) {
	f := Facts{Language: lang}
	cfg := maskConfig{
		lineComments:  []string{"--"},
		blockComments: [][2]string{{"--[[", "]]"}},
		singleQuote:   true,
		doubleQuote:   true,
	}
	code := maskSource(src, cfg)
	plain := maskSource(src, keepStrings(cfg))
	lines := splitSourceLines(src)
	if len(lines) == 0 {
		return f, true
	}

	var stack []lineFrame
	lastContent := 0

	for _, ln := range lines {
		masked := code[ln.start:ln.end]
		trimmed := strings.TrimSpace(string(masked))
		if trimmed == "" {
			continue
		}
		lastContent = ln.num

		plainLine := strings.TrimSpace(string(plain[ln.start:ln.end]))
		for _, m := range reLuaRequire.FindAllStringSubmatch(plainLine, -1) {
			addEdge(&f, "", m[1], EdgeImport, ConfidenceExact, ln.num)
		}

		if reLuaClose.MatchString(trimmed) {
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if top.symIdx >= 0 {
					f.Symbols[top.symIdx].LineEnd = ln.num
				}
			}
			continue
		}

		name := ""
		if m := reLuaFunc.FindStringSubmatch(trimmed); m != nil {
			name = m[1]
		} else if m := reLuaAssignFunc.FindStringSubmatch(trimmed); m != nil {
			name = m[1]
		}
		if name != "" {
			parent, base := splitQualified(name)
			kind := KindFunc
			if parent != "" {
				kind = KindMethod
			}
			sig := flattenSignature(sliceText(plain, ln.start, ln.end))
			f.Symbols = append(f.Symbols, Symbol{
				Name: base, Kind: kind, Parent: parent,
				Signature: sig, LineStart: ln.num, LineEnd: ln.num,
			})
			stack = append(stack, lineFrame{symIdx: len(f.Symbols) - 1, name: base, kind: kind})
			continue
		}
		if reLuaOpen.MatchString(trimmed) {
			stack = append(stack, lineFrame{symIdx: -1, kind: "block"})
		}
	}

	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].symIdx >= 0 {
			f.Symbols[stack[i].symIdx].LineEnd = lastContent
		}
	}
	return f, true
}
