package sensitive

import (
	"regexp"
	"strings"
)

// secretKeyPart 是密钥类字段名的关键词集合。它允许出现在标识符中间
// （db_password、API_KEY、access_token），因此两侧用标识符字符包裹。
const secretKeyPart = `secret|token|password|passwd|pwd|api[_-]?key|private[_-]?key|credential|access[_-]?key`

// secretAssignmentRegex 匹配"密钥字段 = 字面量"的赋值，并拆成三段：
// 1. 行首到分隔符（原样保留），2. 待判定的值，3. 值之后的剩余内容（原样保留）。
//
// 这里刻意不做贪婪的整行替换：历史上用 `(.+)$` 把整行吃掉，
// 再写回不带引号的 <REDACTED>，导致
//
//	APIKey: conf.Embedding.APIKey,   → APIKey: <REDACTED>
//	tokens := splitCommandTokens(..) → tokens :<REDACTED>
//
// 这类普通代码行被改写成非法语法，go/parser 解析失败后整份文件退回盲切。
// 现在只替换"值"这一段，且判定交给 isSecretLiteral。
var secretAssignmentRegex = regexp.MustCompile(
	`(?i)^(\s*(?:(?:export|default|public|private|protected|static|final|readonly|const|var|let|val)\s+)*` +
		`[A-Za-z0-9_\-.]*(?:` + secretKeyPart + `)[A-Za-z0-9_\-.]*\s*(?::=|[:=])\s*)` +
		`("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|` + "`[^`]*`" + `|[^\s,;()\[\]{}]+)`,
)

// redactedValue 是写回的值。带引号是为了在任何语言里都保持合法的字面量：
// 若写成裸的 <REDACTED>，在不接受该标识符的语法位置会直接让文件解析失败。
const redactedValue = `"<REDACTED>"`

func RedActText(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		loc := secretAssignmentRegex.FindStringSubmatchIndex(line)
		if loc == nil {
			continue
		}
		// loc[4]:loc[5] 是第二个捕获组（值）的范围。
		if !isSecretLiteral(line[loc[4]:loc[5]]) {
			continue
		}
		lines[i] = line[:loc[4]] + redactedValue + line[loc[5]:]
	}
	return strings.Join(lines, "\n")
}

// isSecretLiteral 判断赋值右侧是否"像一个硬编码的密钥字面量"。
//
// 这里宁可漏掉也不太愿误伤：误伤会把普通代码改坏（解析失败 → 整份文件退回盲切，
// 引用回溯也对不上号），而漏掉只影响一个公开仓库里本就不敏感的值。
func isSecretLiteral(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	if isQuotedLiteral(v) {
		return true
	}
	// 裸 token：必须同时含字母和数字、长度足够，且不含表达式字符。
	// 这样才能排除 conf.Embedding.APIKey（含 .）、prompt（无数字）、
	// splitCommandTokens（无数字）这类普通标识符。
	if len(v) < 12 {
		return false
	}
	hasLetter, hasDigit := false, false
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == '_' || r == '-' || r == '+' || r == '/' || r == '=':
		default:
			return false
		}
	}
	return hasLetter && hasDigit
}

// isQuotedLiteral 判断整段是否被同一种引号包裹。
func isQuotedLiteral(v string) bool {
	if len(v) < 2 {
		return false
	}
	quote := v[0]
	if quote != '"' && quote != '\'' && quote != '`' {
		return false
	}
	return v[len(v)-1] == quote
}
