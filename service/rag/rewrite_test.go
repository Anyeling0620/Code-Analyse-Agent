package rag

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"edu.agent.code/config"
	"github.com/cloudwego/eino/schema"
)

// TestNeedsRewrite 覆盖改写触发条件：只有"含中文自然语言"的查询才需要改写。
func TestNeedsRewrite(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  bool
	}{
		{"中文自然语言提问", "向量库的连接是怎么建立的", true},
		{"中文短语", "鉴权在哪一层", true},
		{"中英混合但以中文为主", "这个 handler 是怎么注册路由的", true},
		{"纯英文提问", "how does the auth middleware work", false},
		{"纯代码标识符", "NewMilvus", false},
		{"纯标识符带路径", "adaptor/vector/milvus.go", false},
		{"空查询", "", false},
		{"只有空白", "   ", false},
		{"过短的中文", "鉴权", false},
		{"单个汉字", "查", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsRewrite(tc.query); got != tc.want {
				t.Fatalf("NeedsRewrite(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

// TestNewQueryRewriterDisabledReturnsNil 覆盖"关闭时不变"：未启用一律拿到 nil，
// 调用方据此走原始单路检索。
func TestNewQueryRewriterDisabledReturnsNil(t *testing.T) {
	if got := NewQueryRewriter(config.Rewrite{Enabled: false}, config.DeepSeek{}); got != nil {
		t.Fatalf("disabled 时应返回 nil，实际 %+v", got)
	}
	// 启用但 provider 不认识时同样降级为 nil，避免误发请求。
	unknown := config.Rewrite{
		Enabled:  true,
		Provider: "openai",
		BaseURL:  "https://example.com",
		APIKey:   "k",
		Model:    "m",
	}
	if got := NewQueryRewriter(unknown, config.DeepSeek{}); got != nil {
		t.Fatalf("未知 provider 时应返回 nil，实际 %+v", got)
	}
	// 启用但端点/密钥/模型不完整时也返回 nil。
	missing := config.Rewrite{Enabled: true, Provider: RewriteProviderDeepSeek}
	if got := NewQueryRewriter(missing, config.DeepSeek{}); got != nil {
		t.Fatalf("配置不完整时应返回 nil，实际 %+v", got)
	}
}

// TestQueryRewriterFallsBackToDeepSeekConfig 覆盖端点/密钥/模型的兜底取值。
func TestQueryRewriterFallsBackToDeepSeekConfig(t *testing.T) {
	fallback := config.DeepSeek{BaseURL: "https://api.deepseek.com/", APIKey: "sk-x", Model: "deepseek-flash"}
	rewriter := NewQueryRewriter(config.Rewrite{Enabled: true, Provider: RewriteProviderDeepSeek}, fallback)
	if rewriter == nil {
		t.Fatal("应能回退到顶层 deepseek 配置构造改写器")
	}
	if rewriter.baseURL != "https://api.deepseek.com" {
		t.Fatalf("base_url 应去掉结尾斜杠，实际 %q", rewriter.baseURL)
	}
	if rewriter.model != "deepseek-flash" || rewriter.apiKey != "sk-x" {
		t.Fatalf("应回退到 deepseek 的 model/api_key，实际 %q/%q", rewriter.model, rewriter.apiKey)
	}
	if rewriter.maxSubqueries() != rewriteDefaultMaxSubqueries {
		t.Fatalf("未配置 max_subqueries 应取默认值 %d，实际 %d", rewriteDefaultMaxSubqueries, rewriter.maxSubqueries())
	}
}

// TestVariantsDisabledKeepsOriginal 覆盖"关闭时行为完全一致"：nil 改写器只返回原始查询。
func TestVariantsDisabledKeepsOriginal(t *testing.T) {
	var rewriter *QueryRewriter
	got := rewriter.Variants(context.Background(), "向量库的连接是怎么建立的")
	if len(got) != 1 || got[0] != "向量库的连接是怎么建立的" {
		t.Fatalf("关闭时只应返回原始查询，实际 %v", got)
	}
	if rewriter.Enabled() {
		t.Fatal("nil 改写器 Enabled() 应为 false")
	}
}

// TestVariantsSuccessIncludesOriginalAndExtras 覆盖成功路径：
// 原始中文查询必须保留，改写英文与子问题各自成路，且受 max_subqueries 限制。
func TestVariantsSuccessIncludesOriginalAndExtras(t *testing.T) {
	content := `{"rewritten_en":"how the vector store connection is established","keywords":["vector store","milvus","connection"],"subqueries":["where is the milvus client initialized","how is the collection created"]}`
	server := newRewriteServer(t, http.StatusOK, chatCompletionBody(content))
	defer server.Close()

	rewriter := NewQueryRewriter(config.Rewrite{
		Enabled:       true,
		Provider:      RewriteProviderDeepSeek,
		BaseURL:       server.URL,
		APIKey:        "sk-test",
		Model:         "deepseek-flash",
		TimeoutSec:    5,
		MaxSubqueries: 2,
	}, config.DeepSeek{})
	if rewriter == nil {
		t.Fatal("配置完整时应构造出改写器")
	}

	query := "向量库的连接是怎么建立的"
	variants := rewriter.Variants(context.Background(), query)
	if len(variants) == 0 || variants[0] != query {
		t.Fatalf("原始查询必须排在第一位，实际 %v", variants)
	}
	// 1 条原始 + 英文改写 + 关键词串 + 2 条子问题（上限 2 表示 extras 最多 2 条）
	if len(variants) != 3 {
		t.Fatalf("extras 应被 max_subqueries=2 截断，实际 %v", variants)
	}
	if !strings.Contains(strings.ToLower(variants[1]), "vector store") {
		t.Fatalf("第二路应为英文改写，实际 %q", variants[1])
	}
	// 两路英文不得重复。
	if strings.EqualFold(variants[1], variants[2]) {
		t.Fatalf("改写产物不应重复：%v", variants)
	}
}

// TestRewriteParsesFencedJSON 覆盖模型输出带 ```json 代码块时的解析。
func TestRewriteParsesFencedJSON(t *testing.T) {
	content := "```json\n{\"rewritten_en\":\"auth middleware chain\",\"keywords\":[\"auth\"],\"subqueries\":[]}\n```"
	server := newRewriteServer(t, http.StatusOK, chatCompletionBody(content))
	defer server.Close()

	rewriter := newRewriterForServer(server.URL, time.Second, 3)
	result, err := rewriter.Rewrite(context.Background(), "鉴权是在哪一层做的")
	if err != nil {
		t.Fatalf("带代码块的输出应能解析，实际错误 %v", err)
	}
	if result.RewrittenEN != "auth middleware chain" {
		t.Fatalf("rewritten_en 解析有误：%+v", result)
	}
}

// TestRewriteSkipsNonChineseWithoutCallingUpstream 覆盖触发条件：非中文查询不应发出请求。
func TestRewriteSkipsNonChineseWithoutCallingUpstream(t *testing.T) {
	called := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(chatCompletionBody(`{"rewritten_en":"x"}`)))
	}))
	defer server.Close()

	rewriter := newRewriterForServer(server.URL, time.Second, 3)
	for _, query := range []string{"NewMilvus", "how does auth work", "adaptor/vector/milvus.go"} {
		variants := rewriter.Variants(context.Background(), query)
		if len(variants) != 1 || variants[0] != query {
			t.Fatalf("非中文查询 %q 应原样单路返回，实际 %v", query, variants)
		}
	}
	if called != 0 {
		t.Fatalf("非中文查询不应调用改写接口，实际调用 %d 次", called)
	}
}

// TestVariantsDegradesOnHTTPError 覆盖失败降级：上游 5xx 时退回原始查询，不阻断检索。
func TestVariantsDegradesOnHTTPError(t *testing.T) {
	server := newRewriteServer(t, http.StatusInternalServerError, `{"error":{"message":"boom"}}`)
	defer server.Close()

	rewriter := newRewriterForServer(server.URL, time.Second, 3)
	query := "向量库的连接是怎么建立的"
	variants := rewriter.Variants(context.Background(), query)
	if len(variants) != 1 || variants[0] != query {
		t.Fatalf("上游报错时应退回原始查询，实际 %v", variants)
	}
	// Rewrite 本身应把错误暴露给调用方，便于观测。
	if _, err := rewriter.Rewrite(context.Background(), query); err == nil {
		t.Fatal("上游 5xx 时 Rewrite 应返回错误")
	}
}

// TestVariantsDegradesOnTimeout 覆盖超时降级。
func TestVariantsDegradesOnTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(chatCompletionBody(`{"rewritten_en":"late"}`)))
	}))
	defer server.Close()

	rewriter := newRewriterForServer(server.URL, 50*time.Millisecond, 3)
	query := "鉴权是在哪一层做的"
	start := time.Now()
	variants := rewriter.Variants(context.Background(), query)
	if len(variants) != 1 || variants[0] != query {
		t.Fatalf("超时时应退回原始查询，实际 %v", variants)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("超时应尽快返回，实际耗时 %s", elapsed)
	}
}

// TestVariantsDegradesOnMalformedJSON 覆盖上游 200 但内容不是 JSON 的情况。
func TestVariantsDegradesOnMalformedJSON(t *testing.T) {
	server := newRewriteServer(t, http.StatusOK, chatCompletionBody("抱歉，我无法完成这个请求。"))
	defer server.Close()

	rewriter := newRewriterForServer(server.URL, time.Second, 3)
	query := "向量库的连接是怎么建立的"
	variants := rewriter.Variants(context.Background(), query)
	if len(variants) != 1 || variants[0] != query {
		t.Fatalf("内容不可解析时应退回原始查询，实际 %v", variants)
	}
}

// TestRewriteSkippedErrorIsDistinguishable 覆盖 ErrRewriteSkipped 语义。
func TestRewriteSkippedErrorIsDistinguishable(t *testing.T) {
	rewriter := newRewriterForServer("http://127.0.0.1:1", time.Second, 3)
	_, err := rewriter.Rewrite(context.Background(), "NewMilvus")
	if !errors.Is(err, ErrRewriteSkipped) {
		t.Fatalf("非中文查询应返回 ErrRewriteSkipped，实际 %v", err)
	}
}

// TestMergeRRFDedupesAndRanks 覆盖 RRF 融合：多路都命中的 chunk 排最前，且不重复。
func TestMergeRRFDedupesAndRanks(t *testing.T) {
	a := &schema.Document{ID: "a", Content: "a"}
	b := &schema.Document{ID: "b", Content: "b"}
	c := &schema.Document{ID: "c", Content: "c"}
	d := &schema.Document{ID: "d", Content: "d"}
	// a 在两路里都出现，权重最高；b/c/d 各出现一次。
	sets := [][]*schema.Document{{a, b, c}, {d, a}}
	merged := MergeRRF(sets, 0)
	if len(merged) != 4 {
		t.Fatalf("应去重后保留 4 条，实际 %d (%v)", len(merged), ids(merged))
	}
	if merged[0].ID != "a" {
		t.Fatalf("两路都命中的 a 应排第一，实际 %v", ids(merged))
	}
	if merged[0] != a {
		t.Fatal("融合结果应复用原始 document 指针，避免丢失 metadata")
	}
	// 截断参数生效。
	if got := MergeRRF(sets, 2); len(got) != 2 {
		t.Fatalf("k=2 应截断到 2 条，实际 %d", len(got))
	}
}

// TestMergeRRFUsesMetadataWhenIDMissing 覆盖没有主键时的去重键。
func TestMergeRRFUsesMetadataWhenIDMissing(t *testing.T) {
	first := &schema.Document{Content: "x", MetaData: map[string]any{"source_path": "a/b.go", "line_start": float64(10)}}
	second := &schema.Document{Content: "x", MetaData: map[string]any{"source_path": "a/b.go", "line_start": float64(10)}}
	merged := MergeRRF([][]*schema.Document{{first}, {second}}, 0)
	if len(merged) != 1 {
		t.Fatalf("同 source_path+line 应视为同一 chunk，实际 %d", len(merged))
	}
}

// TestMergeRRFHandlesEmptyInputs 覆盖空输入不 panic。
func TestMergeRRFHandlesEmptyInputs(t *testing.T) {
	if got := MergeRRF(nil, 5); len(got) != 0 {
		t.Fatalf("空输入应返回空结果，实际 %v", got)
	}
	if got := MergeRRF([][]*schema.Document{{nil, nil}}, 5); len(got) != 0 {
		t.Fatalf("全 nil 输入应返回空结果，实际 %v", got)
	}
}

// --- 测试辅助 ---

// newRewriterForServer 直接构造改写器，便于注入毫秒级超时（配置层最小单位是秒）。
func newRewriterForServer(baseURL string, timeout time.Duration, maxSubqueries int) *QueryRewriter {
	return &QueryRewriter{
		conf:    config.Rewrite{MaxSubqueries: maxSubqueries},
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  "sk-test",
		model:   "deepseek-flash",
		timeout: timeout,
		client:  &http.Client{Timeout: timeout},
	}
}

func newRewriteServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("改写请求路径应为 /chat/completions，实际 %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer sk-test" {
			t.Errorf("缺少鉴权头，实际 %q", auth)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	return server
}

// chatCompletionBody 把一段文本包装成 OpenAI 兼容的 chat completions 响应。
func chatCompletionBody(content string) string {
	encoded, _ := json.Marshal(content)
	return `{"choices":[{"message":{"role":"assistant","content":` + string(encoded) + `}}]}`
}

func ids(docs []*schema.Document) []string {
	out := make([]string, 0, len(docs))
	for _, doc := range docs {
		out = append(out, doc.ID)
	}
	return out
}
