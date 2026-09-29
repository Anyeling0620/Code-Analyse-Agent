package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"edu.agent.code/config"
	"github.com/cloudwego/eino/schema"
)

func testDocs(contents ...string) []*schema.Document {
	docs := make([]*schema.Document, 0, len(contents))
	for i, content := range contents {
		docs = append(docs, &schema.Document{
			ID:       fmt.Sprintf("id-%d", i),
			Content:  content,
			MetaData: map[string]any{"header": fmt.Sprintf("h%d", i)},
		})
	}
	return docs
}

func TestNewRerankerProviderSelection(t *testing.T) {
	cases := []struct {
		name string
		conf config.Rerank
		want bool
	}{
		{
			name: "bigmodel 已启用且配置完整",
			conf: config.Rerank{Enabled: true, Provider: "bigmodel", BaseURL: "https://example.com/rerank"},
			want: true,
		},
		{
			name: "voyage 已启用且配置完整",
			conf: config.Rerank{Enabled: true, Provider: "voyage", BaseURL: "https://example.com/rerank"},
			want: true,
		},
		{
			name: "llamacpp 仍然可用",
			conf: config.Rerank{Enabled: true, Provider: "llamacpp", BaseURL: "http://127.0.0.1:8081/v1/rerank"},
			want: true,
		},
		{
			name: "provider 大小写与别名可归一化",
			conf: config.Rerank{Enabled: true, Provider: "  ZhipuAI ", BaseURL: "https://example.com/rerank"},
			want: true,
		},
		{
			name: "未启用",
			conf: config.Rerank{Enabled: false, Provider: "bigmodel", BaseURL: "https://example.com/rerank"},
			want: false,
		},
		{
			name: "provider 为空",
			conf: config.Rerank{Enabled: true, Provider: "", BaseURL: "https://example.com/rerank"},
			want: false,
		},
		{
			name: "未知 provider",
			conf: config.Rerank{Enabled: true, Provider: "not-exist", BaseURL: "https://example.com/rerank"},
			want: false,
		},
		{
			name: "base_url 缺失",
			conf: config.Rerank{Enabled: true, Provider: "bigmodel"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewReranker(tc.conf)
			if tc.want && got == nil {
				t.Fatalf("NewReranker() = nil, want non-nil reranker")
			}
			if !tc.want && got != nil {
				t.Fatalf("NewReranker() = %#v, want nil", got)
			}
		})
	}
}

func TestHTTPRerankerBigModelRequestAndResponse(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"index":1,"relevance_score":0.91},{"index":0,"relevance_score":0.42}]}`)
	}))
	defer srv.Close()

	reranker := NewReranker(config.Rerank{
		Enabled:  true,
		Provider: "bigmodel",
		BaseURL:  srv.URL,
		APIKey:   "test-key",
		Model:    "rerank",
		TopN:     2,
	})
	if reranker == nil {
		t.Fatal("NewReranker() = nil, want bigmodel reranker")
	}

	docs := testDocs("alpha", "beta", "gamma")
	got, err := reranker.Rerank(context.Background(), "怎么鉴权", docs, 2)
	if err != nil {
		t.Fatalf("Rerank() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Rerank() returned %d docs, want 2", len(got))
	}
	if got[0].ID != "id-1" || got[1].ID != "id-0" {
		t.Fatalf("Rerank() order = %s,%s want id-1,id-0", got[0].ID, got[1].ID)
	}
	if got[0].Score() != 0.91 {
		t.Fatalf("Rerank() score = %v, want 0.91", got[0].Score())
	}

	// bigmodel 的请求体必须带 model，且字段名是 top_n。
	if gotBody["model"] != "rerank" {
		t.Fatalf("request model = %v, want rerank", gotBody["model"])
	}
	if gotBody["query"] != "怎么鉴权" {
		t.Fatalf("request query = %v", gotBody["query"])
	}
	if _, ok := gotBody["top_n"]; !ok {
		t.Fatalf("request body missing top_n: %v", gotBody)
	}
	if _, ok := gotBody["top_k"]; ok {
		t.Fatalf("request body should not contain top_k for bigmodel: %v", gotBody)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	// header 应前缀拼进 document 文本，保证重排能看到标题上下文。
	documents, _ := gotBody["documents"].([]any)
	if len(documents) != 3 {
		t.Fatalf("request documents len = %d, want 3", len(documents))
	}
	if first, _ := documents[0].(string); !strings.HasPrefix(first, "h0\n") {
		t.Fatalf("document[0] = %q, want header prefix", first)
	}
}

func TestHTTPRerankerVoyageUsesTopKAndDataField(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"index":2,"relevance_score":0.77}]}`)
	}))
	defer srv.Close()

	reranker := NewReranker(config.Rerank{
		Enabled:  true,
		Provider: "voyage",
		BaseURL:  srv.URL,
		Model:    "rerank-3",
		TopN:     1,
	})
	if reranker == nil {
		t.Fatal("NewReranker() = nil, want voyage reranker")
	}

	got, err := reranker.Rerank(context.Background(), "auth middleware", testDocs("a", "b", "c"), 1)
	if err != nil {
		t.Fatalf("Rerank() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != "id-2" {
		t.Fatalf("Rerank() = %v, want single id-2", got)
	}
	if gotBody["model"] != "rerank-3" {
		t.Fatalf("request model = %v, want rerank-3", gotBody["model"])
	}
	if _, ok := gotBody["top_k"]; !ok {
		t.Fatalf("voyage request missing top_k: %v", gotBody)
	}
	if _, ok := gotBody["top_n"]; ok {
		t.Fatalf("voyage request should not contain top_n: %v", gotBody)
	}
}

func TestHTTPRerankerErrorPaths(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantSub string
	}{
		{name: "非 200", status: http.StatusInternalServerError, body: `{"error":{"message":"boom"}}`, wantSub: "500"},
		{name: "坏 JSON", status: http.StatusOK, body: `not-json`, wantSub: "decode response failed"},
		{name: "空结果", status: http.StatusOK, body: `{"results":[]}`, wantSub: "no results"},
		{name: "索引越界", status: http.StatusOK, body: `{"results":[{"index":99,"relevance_score":0.5}]}`, wantSub: "out of range"},
		{name: "响应体业务错误", status: http.StatusOK, body: `{"error":{"message":"invalid api key"}}`, wantSub: "invalid api key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			reranker := NewReranker(config.Rerank{
				Enabled:  true,
				Provider: "bigmodel",
				BaseURL:  srv.URL,
			})
			_, err := reranker.Rerank(context.Background(), "q", testDocs("a"), 1)
			if err == nil {
				t.Fatalf("Rerank() error = nil, want error containing %q", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("Rerank() error = %q, want containing %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestHTTPRerankerTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, `{"results":[{"index":0,"relevance_score":0.5}]}`)
	}))
	defer srv.Close()

	reranker := NewHTTPReranker(RerankProviderBigModel, config.Rerank{BaseURL: srv.URL})
	reranker.client = &http.Client{Timeout: 30 * time.Millisecond}

	if _, err := reranker.Rerank(context.Background(), "q", testDocs("a"), 1); err == nil {
		t.Fatal("Rerank() error = nil, want timeout error")
	}
}

func TestHTTPRerankerEmptyChunksSkipsRequest(t *testing.T) {
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
	}))
	defer srv.Close()

	reranker := NewHTTPReranker(RerankProviderBigModel, config.Rerank{BaseURL: srv.URL})
	got, err := reranker.Rerank(context.Background(), "q", nil, 5)
	if err != nil {
		t.Fatalf("Rerank() error = %v, want nil", err)
	}
	if got != nil {
		t.Fatalf("Rerank() = %v, want nil", got)
	}
	if called.Load() {
		t.Fatal("Rerank() should not call upstream when chunks is empty")
	}
}

func TestHTTPRerankerTopNClampedToCandidateCount(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = io.WriteString(w, `{"results":[{"index":0,"relevance_score":0.9},{"index":1,"relevance_score":0.8}]}`)
	}))
	defer srv.Close()

	reranker := NewHTTPReranker(RerankProviderBigModel, config.Rerank{BaseURL: srv.URL})
	got, err := reranker.Rerank(context.Background(), "q", testDocs("a", "b"), 10)
	if err != nil {
		t.Fatalf("Rerank() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Rerank() returned %d docs, want 2", len(got))
	}
	if topN, _ := gotBody["top_n"].(float64); int(topN) != 2 {
		t.Fatalf("request top_n = %v, want 2 (clamped to candidate count)", gotBody["top_n"])
	}
}

func TestHTTPRerankerHandlesNilChunk(t *testing.T) {
	// 指向 nil 位置的候选应被跳过；只要还有有效结果就正常返回。
	t.Run("跳过 nil 候选", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"results":[{"index":0,"relevance_score":0.1},{"index":1,"relevance_score":0.9}]}`)
		}))
		defer srv.Close()

		reranker := NewHTTPReranker(RerankProviderBigModel, config.Rerank{BaseURL: srv.URL})
		docs := []*schema.Document{nil, {ID: "keep", Content: "x"}}
		got, err := reranker.Rerank(context.Background(), "q", docs, 2)
		if err != nil {
			t.Fatalf("Rerank() error = %v", err)
		}
		if len(got) != 1 || got[0].ID != "keep" {
			t.Fatalf("Rerank() = %v, want single doc keep", got)
		}
	})

	// 全部候选都无效时返回错误（不 panic），让调用方退回未重排结果。
	t.Run("全部候选无效时返回错误而非 panic", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"results":[{"index":0,"relevance_score":0.1}]}`)
		}))
		defer srv.Close()

		reranker := NewHTTPReranker(RerankProviderBigModel, config.Rerank{BaseURL: srv.URL})
		docs := []*schema.Document{nil, {ID: "keep", Content: "x"}}
		if _, err := reranker.Rerank(context.Background(), "q", docs, 2); err == nil {
			t.Fatal("Rerank() error = nil, want out-of-range error")
		}
	})
}
