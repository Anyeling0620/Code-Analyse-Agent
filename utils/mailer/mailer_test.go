package mailer

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"mime"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestEncodeHeaderRFC2047(t *testing.T) {
	encoded := encodeHeader("代码仓库分析工作台邮箱验证码")
	if !strings.Contains(encoded, "=?UTF-8?b?") && !strings.Contains(encoded, "=?UTF-8?B?") {
		t.Fatalf("expected RFC 2047 encoded word, got %q", encoded)
	}
	decoder := new(mime.WordDecoder)
	decoded, err := decoder.DecodeHeader(strings.ReplaceAll(encoded, "\r\n ", " "))
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if decoded != "代码仓库分析工作台邮箱验证码" {
		t.Fatalf("decoded = %q", decoded)
	}
}

func TestBuildMessageMultipart(t *testing.T) {
	raw, err := buildMessage(
		"noreply@example.com", "代码仓库分析工作台", "student@example.com",
		"【代码仓库分析工作台】邮箱验证码", "验证码：123456", "<p>验证码：123456</p>",
	)
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}
	message := string(raw)
	for _, want := range []string{
		"From: =?UTF-8?b?", "<noreply@example.com>",
		"To: student@example.com",
		"Content-Type: multipart/alternative;",
		`Content-Type: text/plain; charset=UTF-8`,
		`Content-Type: text/html; charset=UTF-8`,
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q:\n%s", want, message)
		}
	}
}

// fakeSMTPServer 接受一封邮件并回传 DATA 内容，用于验证 TLS=none 的完整发送链路。
func fakeSMTPServer(t *testing.T) (addr string, received <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	ch := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)
		writeLine := func(line string) {
			_, _ = writer.WriteString(line + "\r\n")
			_ = writer.Flush()
		}
		writeLine("220 localhost ESMTP test")

		var data strings.Builder
		inData := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					t.Logf("smtp read: %v", err)
				}
				break
			}
			trimmed := strings.TrimRight(line, "\r\n")
			if inData {
				if trimmed == "." {
					inData = false
					ch <- data.String()
					writeLine("250 OK queued")
					continue
				}
				data.WriteString(trimmed + "\r\n")
				continue
			}
			upper := strings.ToUpper(trimmed)
			switch {
			case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
				writeLine("250-localhost")
				writeLine("250 OK")
			case strings.HasPrefix(upper, "MAIL FROM"), strings.HasPrefix(upper, "RCPT TO"):
				writeLine("250 OK")
			case strings.HasPrefix(upper, "DATA"):
				inData = true
				writeLine("354 End data with <CR><LF>.<CR><LF>")
			case strings.HasPrefix(upper, "QUIT"):
				writeLine("221 Bye")
				return
			default:
				writeLine("250 OK")
			}
		}
	}()
	return listener.Addr().String(), ch
}

func TestSendPlainSMTP(t *testing.T) {
	addr, received := fakeSMTPServer(t)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	client := New(Config{
		Host:     host,
		Port:     portNumber,
		From:     "noreply@example.com",
		FromName: "代码仓库分析工作台",
		TLS:      "none",
		Timeout:  5 * time.Second,
	})
	if err := client.Send(context.Background(), "student@example.com",
		"【代码仓库分析工作台】邮箱验证码", "你的验证码是 123456", "<p>你的验证码是 123456</p>"); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case message := <-received:
		if !strings.Contains(message, "To: student@example.com") {
			t.Fatalf("missing To header:\n%s", message)
		}
		// 正文是 base64 编码的，解码后应能还原验证码。
		if !strings.Contains(decodeBody(t, message), "123456") {
			t.Fatalf("decoded body missing code:\n%s", message)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for mail")
	}
}

func decodeBody(t *testing.T, message string) string {
	t.Helper()
	var decoded strings.Builder
	for _, line := range strings.Split(message, "\r\n") {
		raw, err := base64.StdEncoding.DecodeString(line)
		if err == nil {
			decoded.Write(raw)
		}
	}
	return decoded.String()
}
