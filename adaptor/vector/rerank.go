package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"edu.agent.code/config"
	"edu.agent.code/utils/logger"
	"github.com/cloudwego/eino/schema"
	"github.com/gogf/gf/v2/util/gconv"
)

// 重排 provider 标识。llamacpp 为本地部署，bigmodel（智谱）与 voyage 为云端服务。
const (
	RerankProviderLlamaCpp = "llamacpp"
	RerankProviderBigModel = "bigmodel"
	RerankProviderVoyage   = "voyage"

	// 重排响应体上限，避免异常响应撑爆内存。
	rerankMaxResponseBytes = 8 << 20
	// 错误信息里回显响应体时的截断长度。
	rerankErrorBodyRunes = 512
)

// IReranker 对召回的候选片段做精排，返回按相关性排序后的片段。
type IReranker interface {
	Rerank(ctx context.Context, query string, chunks []*schema.Document, topN int) ([]*schema.Document, error)
}

// NewReranker 按配置构造重排器。
// 未启用、provider 未知或 base_url 缺失时返回 nil；调用方在拿到 nil 时会跳过重排
// （见 Milvus.Retrieve），即重排不可用时 fail-open，不影响检索主流程。
func NewReranker(conf config.Rerank) IReranker {
	if !conf.Enabled {
		return nil
	}
	provider := normalizeRerankProvider(conf.Provider)
	if provider == "" {
		logger.Warn("rerank disabled: provider is empty")
		return nil
	}
	if !isSupportedRerankProvider(provider) {
		logger.Warn("Unknown reranker provider: %s", conf.Provider)
		return nil
	}
	if strings.TrimSpace(conf.BaseURL) == "" {
		logger.Warn("rerank disabled: base_url is empty, provider=%s", provider)
		return nil
	}
	return NewHTTPReranker(provider, conf)
}

// normalizeRerankProvider 归一化 provider 名称，兼容常见的书写变体。
func normalizeRerankProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "llamacpp", "llama.cpp", "llama-cpp", "llama_cpp":
		return RerankProviderLlamaCpp
	case "bigmodel", "zhipu", "zhipuai", "glm":
		return RerankProviderBigModel
	case "voyage", "voyageai", "voyage-ai":
		return RerankProviderVoyage
	default:
		return ""
	}
}

func isSupportedRerankProvider(provider string) bool {
	switch provider {
	case RerankProviderLlamaCpp, RerankProviderBigModel, RerankProviderVoyage:
		return true
	default:
		return false
	}
}

// HTTPReranker 通过 HTTP 调用重排服务，按 provider 适配请求与响应字段。
type HTTPReranker struct {
	provider string
	baseURL  string
	model    string
	apiKey   string
	client   *http.Client
}

func NewHTTPReranker(provider string, conf config.Rerank) *HTTPReranker {
	timeoutSec := conf.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	return &HTTPReranker{
		provider: provider,
		baseURL:  strings.TrimSpace(conf.BaseURL),
		model:    strings.TrimSpace(conf.Model),
		apiKey:   strings.TrimSpace(conf.APIKey),
		client: &http.Client{
			Timeout: time.Duration(timeoutSec) * time.Second,
		},
	}
}

// standardRerankRequest 用于 llamacpp 与 bigmodel：
// 请求体为 {model, query, documents, top_n}，响应为 results[].index / relevance_score。
type standardRerankRequest struct {
	Model     string   `json:"model,omitempty"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n"`
}

// voyageRerankRequest 用于 voyage：字段名是 top_k 而非 top_n，model 必填，
// 响应为 data[].index / relevance_score。
type voyageRerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopK      int      `json:"top_k"`
}

type rerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type rerankErrorBody struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

type rerankResponse struct {
	Results []rerankResult   `json:"results"` // llamacpp / bigmodel
	Data    []rerankResult   `json:"data"`    // voyage
	Error   *rerankErrorBody `json:"error"`
}

// encodeRequest 按 provider 生成请求体。
func (r *HTTPReranker) encodeRequest(query string, documents []string, topN int) ([]byte, error) {
	if r.provider == RerankProviderVoyage {
		return json.Marshal(voyageRerankRequest{
			Model:     r.model,
			Query:     query,
			Documents: documents,
			TopK:      topN,
		})
	}
	return json.Marshal(standardRerankRequest{
		Model:     r.model,
		Query:     query,
		Documents: documents,
		TopN:      topN,
	})
}

// pickResults 取响应里的候选列表：voyage 用 data，其余用 results；
// 若 provider 配置与实际响应不一致，退回另一个字段，避免因字段差异直接失效。
func (r *HTTPReranker) pickResults(resp rerankResponse) []rerankResult {
	if r.provider == RerankProviderVoyage {
		if len(resp.Data) > 0 {
			return resp.Data
		}
		return resp.Results
	}
	if len(resp.Results) > 0 {
		return resp.Results
	}
	return resp.Data
}

func (r *HTTPReranker) Rerank(ctx context.Context, query string, chunks []*schema.Document, topN int) ([]*schema.Document, error) {
	if len(chunks) == 0 {
		return nil, nil
	}
	if topN <= 0 || topN > len(chunks) {
		topN = len(chunks)
	}

	documents := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		if chunk == nil {
			documents = append(documents, "")
			continue
		}
		header := ""
		if chunk.MetaData != nil {
			header = gconv.String(chunk.MetaData["header"])
		}
		documents = append(documents, header+"\n"+chunk.Content)
	}

	body, err := r.encodeRequest(query, documents, topN)
	if err != nil {
		return nil, fmt.Errorf("rerank marshal request failed: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rerank build request failed: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if r.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+r.apiKey)
	}

	resp, err := r.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("rerank request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, rerankMaxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("rerank read response failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rerank http status %s: %s", resp.Status, truncateForError(string(raw)))
	}

	var parsed rerankResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("rerank decode response failed: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return nil, fmt.Errorf("rerank upstream error: %s", parsed.Error.Message)
	}

	results := r.pickResults(parsed)
	if len(results) == 0 {
		return nil, fmt.Errorf("rerank response contains no results")
	}

	reranked := make([]*schema.Document, 0, len(results))
	seen := make(map[int]bool, len(results))
	for _, item := range results {
		if item.Index < 0 || item.Index >= len(chunks) || seen[item.Index] {
			continue
		}
		chunk := chunks[item.Index]
		if chunk == nil {
			continue
		}
		seen[item.Index] = true
		chunk.WithScore(item.RelevanceScore)
		reranked = append(reranked, chunk)
	}
	if len(reranked) == 0 {
		return nil, fmt.Errorf("rerank response indexes out of range, chunks=%d", len(chunks))
	}
	if len(reranked) > topN {
		reranked = reranked[:topN]
	}
	return reranked, nil
}

func truncateForError(body string) string {
	body = strings.TrimSpace(body)
	runes := []rune(body)
	if len(runes) <= rerankErrorBodyRunes {
		return body
	}
	return string(runes[:rerankErrorBodyRunes]) + "...<truncated>"
}
