package http_request

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	defaultHTTPTimeout = time.Second * 15
	maxHttpTimeout     = time.Second * 60
	maxHttpBodyRunes   = 12000
)

type HTTPRequestInput struct {
	Method     string            `json:"method,omitempty" jsonschema:"description=HTTP 方法，默认 GET"`
	URL        string            `json:"url" jsonschema:"required,description=完整请求地址，必须包含 http 或 https 协议"`
	Headers    map[string]string `json:"headers,omitempty" jsonschema:"description=请求头键值对"`
	Body       string            `json:"body,omitempty" jsonschema:"description=请求体文本，适合 JSON 或普通文本"`
	TimeoutSec int               `json:"timeout_sec,omitempty" jsonschema:"description=超时时间秒数，默认15，最大60"`
}

func NewTool() (tool.BaseTool, error) {
	return toolutils.InferTool(
		"http_request",
		"发起通用 HTTP 请求，适合调用 API、检查接口返回、访问本地服务或第三方 Web 接口。\n"+
			"返回头一行是标量 status / content_type / elapsed_ms / size_bytes；\n"+
			"随后用 dashed section 给出 headers 和 body，body 超大时会截断并标注。",
		func(ctx context.Context, input HTTPRequestInput) (string, error) {
			return runHTTPRequest(ctx, input)
		},
	)
}

func isAllowedMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodTrace:
		return true
	default:
		return false
	}
}

func runHTTPRequest(ctx context.Context, input HTTPRequestInput) (string, error) {
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	if method == "" {
		method = http.MethodGet
	}
	if !isAllowedMethod(method) {
		return "", fmt.Errorf("http method %s not allowed", method)
	}

	targetURL := strings.TrimSpace(input.URL)
	if targetURL == "" {
		return "", fmt.Errorf("url couldn't be empty")
	}
	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		return "", fmt.Errorf("url parse error: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return "", fmt.Errorf("url scheme must be http or https")
	}
	timeout := defaultHTTPTimeout
	if input.TimeoutSec > 0 {
		timeout = time.Duration(input.TimeoutSec) * time.Second
	}
	if timeout > maxHttpTimeout {
		timeout = maxHttpTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, parsedURL.String(), strings.NewReader(input.Body))
	if err != nil {
		return "", fmt.Errorf("build request error: %w", err)
	}
	for key, value := range input.Headers {
		req.Header.Set(key, value)
	}
	if input.Body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	startedAt := time.Now()
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", fmt.Errorf("http request timeout")
		}
		return "", fmt.Errorf("http request error: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("http response body read error: %w", err)
	}
	elapsed := time.Since(startedAt)
	return formatHTTPRequestResult(resp, string(respBody), elapsed), nil
}

func formatHTTPRequestResult(resp *http.Response, body string, elapsed time.Duration) string {
	contentType := resp.Header.Get("Content-Type")
	limited, truncated := limitHTTPBody(body)
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "status=%d content_type=%s elapse=%d size_bytes=%d",
		resp.StatusCode, contentType, elapsed.Milliseconds(), len(body))
	if truncated {
		b.WriteString("truncated=true ")
	}
	b.WriteString("\n")
	b.WriteString("\n--- headers ---\n")
	b.WriteString(formatHeaders(resp.Header))
	b.WriteString("\n\n--- body ---\n")
	if limited == "" {
		b.WriteString("(empty)")
	} else {
		b.WriteString(limited)
	}
	return b.String()
}

func formatHeaders(headers http.Header) string {
	if len(headers) == 0 {
		return "(empty)"
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	for _, k := range keys {
		buf.WriteString(k)
		buf.WriteString(": ")
		buf.WriteString(strings.Join(headers[k], ","))
		buf.WriteString("\n")
	}
	return strings.TrimRight(buf.String(), "\n")
}

func limitHTTPBody(body string) (string, bool) {
	trimmed := strings.TrimRight(body, "\r\n")
	if len(trimmed) == 0 {
		return "", false
	}
	runes := []rune(trimmed)
	if len(runes) <= maxHttpBodyRunes {
		return trimmed, false
	}
	return string(runes[:maxHttpBodyRunes]) + "\n...<response truncated>", true
}
