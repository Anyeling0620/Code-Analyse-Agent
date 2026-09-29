package sensitive

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// 真正的密钥字面量必须被脱敏。
func TestRedActTextRedactsSecretLiterals(t *testing.T) {
	cases := []string{
		`API_KEY=sk-abc123xyz789`,
		`password: "hunter2"`,
		`token: "ghp_abcdefghijklmnop0123"`,
		`db_password = 'correct-horse-1'`,
		`ACCESS_KEY = "AKIA1234567890ABCD"`,
		`const SECRET_TOKEN = "abcdef1234567890"`,
	}
	for _, line := range cases {
		got := RedActText(line)
		if !strings.Contains(got, "<REDACTED>") {
			t.Errorf("应当脱敏但未脱敏: %q -> %q", line, got)
		}
	}
}

// 这些是普通代码行。历史上它们被整行替换成非法语法，
// 导致 go/parser 解析失败、整份文件退回盲切。
func TestRedActTextLeavesOrdinaryCodeAlone(t *testing.T) {
	cases := []string{
		`tokens := splitCommandTokens(segment)`,
		`APIKey:     conf.Embedding.APIKey,`,
		`PromptTokens:     prompt,`,
		`Token: token,`,
		`SecretKey: secrets.Manager,`,
		`Password: cfg.Password, // 从配置读取`,
		`var tokenCount int`,
		`func (s *Service) RevokeToken(id string) error {`,
	}
	for _, line := range cases {
		if got := RedActText(line); got != line {
			t.Errorf("普通代码行不应被改动:\n  原: %q\n  后: %q", line, got)
		}
	}
}

// 脱敏写回的值必须是合法字面量，否则整份 Go 文件的解析会失败。
func TestRedActTextKeepsGoParseable(t *testing.T) {
	src := "package demo\n\n" +
		"type conf struct {\n" +
		"\tAPIKey       string\n" +
		"\tPromptTokens int\n" +
		"}\n" +
		"\n" +
		"func build(embedding conf, prompt int) conf {\n" +
		"\ttokens := splitCommandTokens(\"a b c\")\n" +
		"\t_ = tokens\n" +
		"\treturn conf{\n" +
		"\t\tAPIKey:       \"sk-live-abcdef123456\",\n" +
		"\t\tPromptTokens: prompt,\n" +
		"\t}\n" +
		"}\n" +
		"\n" +
		"func splitCommandTokens(s string) []string { return []string{s} }\n"

	redacted := RedActText(src)
	if !strings.Contains(redacted, "<REDACTED>") {
		t.Fatal("硬编码密钥应当被脱敏")
	}

	if _, err := parser.ParseFile(token.NewFileSet(), "demo.go", redacted, 0); err != nil {
		t.Fatalf("脱敏后 Go 源码应当仍可解析，实际报错: %v\n---\n%s", err, redacted)
	}
}

// 保持既有行为：多行文本逐行处理，非赋值行原样保留。
func TestRedActTextHandlesMultiLine(t *testing.T) {
	in := "line one\nsecret_key = \"abc123\"\nline three"
	out := RedActText(in)
	if !strings.Contains(out, "line one") || !strings.Contains(out, "line three") {
		t.Errorf("非密钥行不应被移除: %q", out)
	}
	if !strings.Contains(out, `secret_key = "<REDACTED>"`) {
		t.Errorf("赋值行应只替换值: %q", out)
	}
}
