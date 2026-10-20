package codeindex

import (
	"path/filepath"
	"strings"
)

// 语言标识。通过 LanguageFor 返回，也写入 Facts.Language。
// 只做扩展名映射，不代表该语言一定有 extractor。
const (
	langGo         = "go"
	langPython     = "python"
	langJavaScript = "javascript"
	langTypeScript = "typescript"
	langJava       = "java"
	langKotlin     = "kotlin"
	langScala      = "scala"
	langCSharp     = "csharp"
	langC          = "c"
	langCPP        = "cpp"
	langRust       = "rust"
	langSwift      = "swift"
	langDart       = "dart"
	langPHP        = "php"
	langRuby       = "ruby"
	langLua        = "lua"
)

var extLanguages = map[string]string{
	".go":    langGo,
	".py":    langPython,
	".pyi":   langPython,
	".js":    langJavaScript,
	".jsx":   langJavaScript,
	".mjs":   langJavaScript,
	".cjs":   langJavaScript,
	".ts":    langTypeScript,
	".tsx":   langTypeScript,
	".mts":   langTypeScript,
	".cts":   langTypeScript,
	".java":  langJava,
	".kt":    langKotlin,
	".kts":   langKotlin,
	".scala": langScala,
	".sc":    langScala,
	".cs":    langCSharp,
	".c":     langC,
	".h":     langC,
	".cc":    langCPP,
	".cpp":   langCPP,
	".cxx":   langCPP,
	".hpp":   langCPP,
	".hh":    langCPP,
	".hxx":   langCPP,
	".rs":    langRust,
	".swift": langSwift,
	".dart":  langDart,
	".php":   langPHP,
	".rb":    langRuby,
	".lua":   langLua,
}

// LanguageFor 按扩展名判定语言；不认识的扩展名返回 ("", false)。
func LanguageFor(relPath string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(relPath))
	lang, ok := extLanguages[ext]
	return lang, ok
}
