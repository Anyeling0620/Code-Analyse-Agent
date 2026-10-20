package codeindex

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

const (
	maxSourceBytes    = 2 << 20 // 2MB
	maxSourceLines    = 50000
	maxSignatureRunes = 200
)

// quoteSingle/newlineByte 用数值常量表达，避免源码中出现转义字符。
const (
	quoteSingle   byte = 0x27
	newlineByte   byte = 0x0A
	backslashByte byte = 0x5C
)

// Extract 抽取单个文件的符号与关系；不支持的语言返回 ok=false。
// 解析异常不会 panic，最差返回空 Facts + false。
func Extract(relPath string, src []byte) (facts Facts, ok bool) {
	lang, known := LanguageFor(relPath)
	if !known {
		return Facts{}, false
	}
	if len(src) > maxSourceBytes || bytes.Count(src, []byte{'\n'}) > maxSourceLines {
		return Facts{Language: lang}, false
	}
	defer func() {
		if r := recover(); r != nil {
			facts = Facts{Language: lang}
			ok = false
		}
	}()
	switch lang {
	case langGo:
		return extractGo(lang, src)
	case langPython:
		return extractPython(lang, src)
	case langRuby:
		return extractRuby(lang, src)
	case langLua:
		return extractLua(lang, src)
	}
	spec := braceSpecs[lang]
	if spec == nil {
		return Facts{Language: lang}, false
	}
	return extractBrace(lang, spec, src)
}

// sourceLine 描述一行源码的字节区间（end 不含换行符）。
type sourceLine struct {
	start int
	end   int
	num   int // 1-based
}

func splitSourceLines(src []byte) []sourceLine {
	if len(src) == 0 {
		return nil
	}
	out := make([]sourceLine, 0, bytes.Count(src, []byte{'\n'})+1)
	start, num := 0, 1
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			out = append(out, sourceLine{start: start, end: i, num: num})
			start = i + 1
			num++
		}
	}
	if start < len(src) {
		out = append(out, sourceLine{start: start, end: len(src), num: num})
	}
	return out
}

// flattenSignature 压平成单行：连续空白合成一个空格，超长截断。
func flattenSignature(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) > maxSignatureRunes {
		s = string([]rune(s)[:maxSignatureRunes]) + "..."
	}
	return s
}

func sliceText(src []byte, start, end int) string {
	if start < 0 {
		start = 0
	}
	if end > len(src) {
		end = len(src)
	}
	if start >= end {
		return ""
	}
	return string(src[start:end])
}

// maskConfig 描述一种语言的注释/字符串语法，供 maskSource 使用。
type maskConfig struct {
	lineComments  []string
	blockComments [][2]string
	singleQuote   bool
	doubleQuote   bool
	backtick      bool
	tripleQuote   bool
	regexLiteral  bool
	rustLifetime  bool
	keepStrings   bool
}

// keepStrings 返回仅屏蔽注释的配置，用于提取 import 等需要原始字符串的场景。
func keepStrings(c maskConfig) maskConfig {
	c.keepStrings = true
	return c
}

// maskSource 返回与 src 等长的副本：注释与字符串内容被替换成空格（换行保留），
// 这样按行做正则匹配时不会命中注释/字符串里的假声明。字节偏移保持不变。
func maskSource(src []byte, cfg maskConfig) []byte {
	out := make([]byte, len(src))
	copy(out, src)
	i := 0
	for i < len(src) {
		if src[i] == '\n' {
			i++
			continue
		}
		if open, closeTok, ok := matchBlockComment(src, i, cfg.blockComments); ok {
			j := bytes.Index(src[i+len(open):], []byte(closeTok))
			end := len(src)
			if j >= 0 {
				end = i + len(open) + j + len(closeTok)
			}
			blankRegion(out, i, end)
			i = end
			continue
		}
		if lc, ok := matchPrefix(src, i, cfg.lineComments); ok {
			j := i + len(lc)
			for j < len(src) && src[j] != '\n' {
				j++
			}
			blankRegion(out, i, j)
			i = j
			continue
		}
		if cfg.tripleQuote && (bytes.HasPrefix(src[i:], []byte{'"', '"', '"'}) ||
			bytes.HasPrefix(src[i:], []byte("'''"))) {
			delim := string(src[i : i+3])
			j := bytes.Index(src[i+3:], []byte(delim))
			end := len(src)
			if j >= 0 {
				end = i + 3 + j + 3
			}
			if !cfg.keepStrings {
				blankRegion(out, i, end)
			}
			i = end
			continue
		}
		c := src[i]
		if (c == '"' && cfg.doubleQuote) || (c == quoteSingle && cfg.singleQuote) || (c == '`' && cfg.backtick) {
			end := scanQuoted(src, i, c)
			if !cfg.keepStrings {
				blankRegion(out, i, end)
			}
			i = end
			continue
		}
		if c == quoteSingle && cfg.rustLifetime {
			if end, isChar := scanRustChar(src, i); isChar {
				if !cfg.keepStrings {
					blankRegion(out, i, end)
				}
				i = end
				continue
			}
			i++
			continue
		}
		if c == '/' && cfg.regexLiteral && looksLikeRegexStart(out, i) {
			if end, isRegex := scanRegexLiteral(src, i); isRegex {
				if !cfg.keepStrings {
					blankRegion(out, i, end)
				}
				i = end
				continue
			}
		}
		i++
	}
	return out
}

func matchPrefix(src []byte, i int, prefixes []string) (string, bool) {
	for _, p := range prefixes {
		if p != "" && bytes.HasPrefix(src[i:], []byte(p)) {
			return p, true
		}
	}
	return "", false
}

func matchBlockComment(src []byte, i int, pairs [][2]string) (string, string, bool) {
	for _, p := range pairs {
		if p[0] != "" && bytes.HasPrefix(src[i:], []byte(p[0])) {
			return p[0], p[1], true
		}
	}
	return "", "", false
}

func blankRegion(out []byte, start, end int) {
	if start < 0 {
		start = 0
	}
	if end > len(out) {
		end = len(out)
	}
	for k := start; k < end; k++ {
		if out[k] != newlineByte {
			out[k] = ' '
		}
	}
}

// scanQuoted 从 src[start] 的引号开始，返回字面量结束后的偏移（未闭合则到行尾）。
func scanQuoted(src []byte, start int, quote byte) int {
	i := start + 1
	for i < len(src) {
		switch src[i] {
		case backslashByte:
			i += 2
		case quote:
			return i + 1
		case newlineByte:
			if quote != '`' {
				return i
			}
			i++
		default:
			i++
		}
	}
	return len(src)
}

// scanRustChar 判断 ' 是字符字面量还是生命周期标记。
func scanRustChar(src []byte, start int) (int, bool) {
	i := start + 1
	if i >= len(src) {
		return 0, false
	}
	if src[i] == backslashByte {
		i++
		if i >= len(src) {
			return 0, false
		}
		if src[i] == 'u' || src[i] == 'x' {
			for i < len(src) && src[i] != quoteSingle && src[i] != newlineByte {
				i++
			}
		} else {
			i++
		}
	} else {
		i++
	}
	if i < len(src) && src[i] == quoteSingle {
		return i + 1, true
	}
	return 0, false
}

func looksLikeRegexStart(out []byte, i int) bool {
	for j := i - 1; j >= 0; j-- {
		switch out[j] {
		case ' ', '	', '':
			continue
		case newlineByte:
			return false
		case '=', '(', ',', ':', '[', '!', '&', '|', '?', '{', '}', ';', '+', '-', '*', '%', '~', '^', '<', '>':
			return true
		default:
			return false
		}
	}
	return true
}

func scanRegexLiteral(src []byte, start int) (int, bool) {
	inClass := false
	for i := start + 1; i < len(src); i++ {
		switch src[i] {
		case backslashByte:
			i++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if inClass {
				continue
			}
			i++
			for i < len(src) && (src[i] == 'g' || src[i] == 'i' || src[i] == 'm' || src[i] == 's' ||
				src[i] == 'u' || src[i] == 'y' || src[i] == 'd' || src[i] == 'v') {
				i++
			}
			return i, true
		case newlineByte:
			return 0, false
		}
	}
	return 0, false
}

// addEdge 追加一条边，过滤空目标。
func addEdge(f *Facts, from, to, kind, confidence string, line int) {
	to = strings.TrimSpace(to)
	if to == "" {
		return
	}
	f.Edges = append(f.Edges, Edge{
		FromSymbol: from,
		ToRaw:      to,
		Kind:       kind,
		Confidence: confidence,
		Line:       line,
	})
}
