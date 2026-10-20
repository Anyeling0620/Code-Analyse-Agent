// Package mailer 是验证码邮件的极简 SMTP 发送器。
//
// 只依赖标准库 net/smtp，不引入第三方邮件库。支持三种连接模式：
//   - ssl：默认，隐式 TLS（常用端口 465，tls.Dial + smtp.NewClient）；
//   - starttls：明文连接后升级 TLS（常用端口 587）；
//   - none：明文连接，仅用于本地测试。
//
// 邮件主题与显示名使用 RFC 2047 编码，正文使用 UTF-8 的 multipart/alternative
// （纯文本 + 简单 HTML）。验证码只出现在邮件正文里，本包不写任何日志。
package mailer

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

const (
	defaultTimeout = 10 * time.Second
	// encodedWordPayload 是单个 RFC 2047 编码字允许承载的原始字节上限：
	// 12 字节固定前缀后缀 + base64(45B)=60，总计 72，小于 RFC 规定的 75。
	encodedWordPayload = 45
	// base64LineLen 是正文 base64 每行字符数（MIME 常见 76 列）。
	base64LineLen = 76
)

// Config 是 SMTP 连接参数。
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	FromName string
	TLS      string // ssl / starttls / none
	Timeout  time.Duration
}

// Mailer 按固定 SMTP 配置发送纯文本 + HTML 邮件。
type Mailer struct {
	cfg Config
}

// New 构造 Mailer；配置在发送时再校验，便于服务启动阶段容忍占位配置。
func New(cfg Config) *Mailer {
	return &Mailer{cfg: cfg}
}

// Send 向 to 发送一封 multipart/alternative 邮件。
// subject / textBody / htmlBody 由调用方生成；本函数不记录它们的内容。
func (m *Mailer) Send(ctx context.Context, to, subject, textBody, htmlBody string) error {
	if m == nil {
		return errors.New("smtp is not configured")
	}
	from, err := parseAddress("from", m.cfg.From)
	if err != nil {
		return err
	}
	recipient, err := parseAddress("to", to)
	if err != nil {
		return err
	}
	if strings.ContainsAny(subject, "\r\n") || strings.ContainsAny(m.cfg.FromName, "\r\n") {
		return errors.New("mail header contains illegal line break")
	}

	message, err := buildMessage(from, m.cfg.FromName, recipient, subject, textBody, htmlBody)
	if err != nil {
		return err
	}

	timeout := m.cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	addr := net.JoinHostPort(strings.TrimSpace(m.cfg.Host), fmt.Sprintf("%d", m.port()))
	conn, err := m.dial(ctx, addr, timeout)
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", addr, err)
	}
	defer conn.Close()
	// 给整段 SMTP 对话一个总超时，避免服务器不响应时请求 goroutine 永久挂住。
	_ = conn.SetDeadline(time.Now().Add(timeout))

	client, err := smtp.NewClient(conn, strings.TrimSpace(m.cfg.Host))
	if err != nil {
		return fmt.Errorf("smtp handshake: %w", err)
	}
	defer client.Close()

	if m.tlsMode() == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("smtp server does not support STARTTLS")
		}
		if err := client.StartTLS(&tls.Config{ServerName: strings.TrimSpace(m.cfg.Host)}); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if strings.TrimSpace(m.cfg.Username) != "" {
		auth := smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, strings.TrimSpace(m.cfg.Host))
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := client.Rcpt(recipient); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := writer.Write(message); err != nil {
		_ = writer.Close()
		return fmt.Errorf("smtp write body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("smtp finish body: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("smtp quit: %w", err)
	}
	return nil
}

func (m *Mailer) port() int {
	if m.cfg.Port > 0 {
		return m.cfg.Port
	}
	if m.tlsMode() == "starttls" {
		return 587
	}
	return 465
}

func (m *Mailer) tlsMode() string {
	switch strings.ToLower(strings.TrimSpace(m.cfg.TLS)) {
	case "starttls":
		return "starttls"
	case "none":
		return "none"
	default:
		return "ssl"
	}
}

func (m *Mailer) dial(ctx context.Context, addr string, timeout time.Duration) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: timeout}
	if m.tlsMode() != "ssl" {
		return dialer.DialContext(ctx, "tcp", addr)
	}
	tlsDialer := &tls.Dialer{
		NetDialer: dialer,
		Config:    &tls.Config{ServerName: strings.TrimSpace(m.cfg.Host)},
	}
	return tlsDialer.DialContext(ctx, "tcp", addr)
}

// buildMessage 生成 MIME 邮件原文，使用 CRLF 换行。
func buildMessage(from, fromName, to, subject, textBody, htmlBody string) ([]byte, error) {
	boundary, err := randomBoundary()
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	writeHeader(&b, "From", formatAddress(from, fromName))
	writeHeader(&b, "To", to)
	writeHeader(&b, "Subject", encodeHeader(subject))
	writeHeader(&b, "Date", time.Now().Format(time.RFC1123Z))
	writeHeader(&b, "MIME-Version", "1.0")
	writeHeader(&b, "Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")

	writePart(&b, boundary, "text/plain; charset=UTF-8", textBody)
	writePart(&b, boundary, "text/html; charset=UTF-8", htmlBody)
	b.WriteString("--" + boundary + "--\r\n")
	return []byte(b.String()), nil
}

func writeHeader(b *strings.Builder, name, value string) {
	b.WriteString(name)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteString("\r\n")
}

func writePart(b *strings.Builder, boundary, contentType, body string) {
	b.WriteString("--" + boundary + "\r\n")
	writeHeader(b, "Content-Type", contentType)
	writeHeader(b, "Content-Transfer-Encoding", "base64")
	b.WriteString("\r\n")
	b.WriteString(wrapBase64(body))
	b.WriteString("\r\n")
}

// encodeHeader 把主题/显示名编码成 RFC 2047 编码字。
// 中文等非 ASCII 内容用 UTF-8 + base64；长文本按编码字长度上限切分并用
// CRLF + 空格折叠，保证每一行都符合 SMTP 的 78 列建议。
func encodeHeader(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if isASCII(raw) {
		return raw
	}
	var words []string
	var chunk strings.Builder
	chunkBytes := 0
	flush := func() {
		if chunk.Len() == 0 {
			return
		}
		words = append(words, mime.BEncoding.Encode("UTF-8", chunk.String()))
		chunk.Reset()
		chunkBytes = 0
	}
	for _, r := range raw {
		size := len(string(r))
		if chunkBytes+size > encodedWordPayload {
			flush()
		}
		chunk.WriteRune(r)
		chunkBytes += size
	}
	flush()
	return strings.Join(words, "\r\n ")
}

func formatAddress(address, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return address
	}
	return encodeHeader(name) + " <" + address + ">"
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func wrapBase64(body string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	if len(encoded) <= base64LineLen {
		return encoded + "\r\n"
	}
	var b strings.Builder
	for len(encoded) > base64LineLen {
		b.WriteString(encoded[:base64LineLen])
		b.WriteString("\r\n")
		encoded = encoded[base64LineLen:]
	}
	b.WriteString(encoded)
	b.WriteString("\r\n")
	return b.String()
}

func randomBoundary() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "=_repoagent_" + base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

func parseAddress(field, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\r\n") {
		return "", fmt.Errorf("invalid %s address", field)
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil || addr.Address == "" {
		return "", fmt.Errorf("invalid %s address", field)
	}
	return addr.Address, nil
}
