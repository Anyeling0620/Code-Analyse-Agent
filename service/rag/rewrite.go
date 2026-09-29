package rag

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"edu.agent.code/config"
	"edu.agent.code/utils/logger"
	"github.com/cloudwego/eino/schema"
)

// 中文查询改写：把"中文自然语言提问"补一路英文检索式与子问题，与原始中文查询并行召回，
// 结果用 RRF 融合后交给既有重排。默认关闭（rewrite.enabled: false），
// 关闭、未配置、超时、报错这四种情况都退回原始查询，保证检索主流程不受影响。

const (
	// RewriteProviderDeepSeek 是当前唯一支持的中文查询改写供应方，走 OpenAI 兼容的 /chat/completions。
	RewriteProviderDeepSeek = "deepseek"

	rewriteDefaultTimeoutSec    = 20
	rewriteDefaultMaxSubqueries = 3
	// minRewriteQueryRunes 是触发改写的最短查询长度（按 rune 计）。
	minRewriteQueryRunes = 4
	// minRewriteCJKRunes 是触发改写所需的最少汉字数，用来排除"代码符号 + 个别中文"的查询。
	minRewriteCJKRunes = 2
	// rewriteMaxResponseBytes 限制改写响应体大小，异常响应不至于撑爆内存。
	rewriteMaxResponseBytes = 1 << 20
	// rewriteErrorBodyRunes 是错误信息里回显响应体的截断长度。
	rewriteErrorBodyRunes = 256
	// rrfK 是 RRF 融合的平滑常数，与 Milvus 混合检索内置的 RRF 保持同一量级。
	rrfK = 60
)

// ErrRewriteSkipped 表示这次查询不满足改写触发条件（纯英文、纯标识符、过短等），
// 调用方看到它应当直接使用原始查询，而不是当成故障。
var ErrRewriteSkipped = errors.New("query rewrite skipped")

// rewritePrompt 要求模型只输出一个 JSON 对象，这样即使端点不支持 response_format 也能稳定解析。
const rewritePrompt = `你是代码检索助手。用户的提问是中文自然语言，需要被改写成更适合在英文代码库里检索的形式。
只输出一个 JSON 对象，不要输出解释、前后缀或 Markdown 代码块，格式固定为：
{"rewritten_en":"...","keywords":["..."],"subqueries":["..."]}
要求：
1. rewritten_en：把问题改写成一句英文检索式，优先使用代码库里可能出现的英文术语与标识符写法（例如 auth middleware、route registration）。
2. keywords：3 到 6 个英文或代码风格的关键词。
3. subqueries：仅当问题包含多个相互独立的意图时才拆分，最多 %d 个英文子问题；只有一个意图时返回空数组。
4. 用户问题里出现的代码标识符按原样保留，不要翻译、不要改写大小写。`

// RewriteResult 是一次查询改写的结构化结果。
type RewriteResult struct {
	RewrittenEN string   `json:"rewritten_en"`
	Keywords    []string `json:"keywords"`
	Subqueries  []string `json:"subqueries"`
}

// QueryRewriter 负责一次 LLM 调用的中文查询改写。
// 它是并发安全的：除了构造期写入的配置外没有可变状态，可以被多个会话共享。
type QueryRewriter struct {
	conf    config.Rewrite
	baseURL string
	apiKey  string
	model   string
	timeout time.Duration
	client  *http.Client
}

// NewQueryRewriter 按配置构造改写器。
// 未启用、provider 不支持、或端点/密钥/模型缺失时返回 nil，调用方在拿到 nil 时跳过改写，
// 也就是 fail-open：改写不可用不影响检索。
func NewQueryRewriter(conf config.Rewrite, fallback config.DeepSeek) *QueryRewriter {
	if !conf.Enabled {
		return nil
	}
	provider := strings.ToLower(strings.TrimSpace(conf.Provider))
	if provider != "" && provider != RewriteProviderDeepSeek {
		logger.Warn("query rewrite disabled: unsupported provider=%s", conf.Provider)
		return nil
	}
	baseURL := firstNonEmpty(conf.BaseURL, fallback.BaseURL)
	apiKey := firstNonEmpty(conf.APIKey, fallback.APIKey)
	model := firstNonEmpty(conf.Model, fallback.Model)
	if baseURL == "" || apiKey == "" || model == "" {
		logger.Warn("query rewrite disabled: base_url/api_key/model 不完整（provider=%s）", provider)
		return nil
	}
	timeoutSec := conf.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = rewriteDefaultTimeoutSec
	}
	timeout := time.Duration(timeoutSec) * time.Second
	return &QueryRewriter{
		conf:    conf,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		timeout: timeout,
		client:  &http.Client{Timeout: timeout},
	}
}

// Enabled 报告改写器是否可用；nil 接收者返回 false，方便调用方直接判空。
func (r *QueryRewriter) Enabled() bool {
	return r != nil
}

// maxSubqueries 返回配置允许的子问题数上限，取默认值兜底。
func (r *QueryRewriter) maxSubqueries() int {
	if r.conf.MaxSubqueries > 0 {
		return r.conf.MaxSubqueries
	}
	return rewriteDefaultMaxSubqueries
}

// NeedsRewrite 判断一个查询是否值得做中文查询改写。
// 只有"含汉字且像自然语言提问"的查询才改写：纯英文、纯标识符、纯符号或过短的查询
// 在 dense + BM25 混合检索下本来就命中良好，改写只会增加成本与噪声。
func NeedsRewrite(query string) bool {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return false
	}
	runes := []rune(trimmed)
	if len(runes) < minRewriteQueryRunes {
		return false
	}
	cjk := 0
	for _, r := range runes {
		if unicode.Is(unicode.Han, r) {
			cjk++
		}
	}
	return cjk >= minRewriteCJKRunes
}

// Rewrite 调一次 LLM 得到结构化改写结果。
// 查询不满足触发条件时返回 ErrRewriteSkipped；其余错误（超时、非 2xx、解析失败）也直接返回，
// 由调用方决定降级。它本身不写日志、不重试，保持行为可预期。
func (r *QueryRewriter) Rewrite(ctx context.Context, query string) (RewriteResult, error) {
	var empty RewriteResult
	if r == nil {
		return empty, ErrRewriteSkipped
	}
	if !NeedsRewrite(query) {
		return empty, ErrRewriteSkipped
	}
	reqCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	payload := map[string]any{
		"model":       r.model,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": fmt.Sprintf(rewritePrompt, r.maxSubqueries())},
			{"role": "user", "content": strings.TrimSpace(query)},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return empty, fmt.Errorf("query rewrite marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, r.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return empty, fmt.Errorf("query rewrite build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.apiKey)

	resp, err := r.client.Do(req)
	if err != nil {
		return empty, fmt.Errorf("query rewrite request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, rewriteMaxResponseBytes))
	if err != nil {
		return empty, fmt.Errorf("query rewrite read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return empty, fmt.Errorf("query rewrite http %d: %s", resp.StatusCode, truncateRunes(string(raw), rewriteErrorBodyRunes))
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return empty, fmt.Errorf("query rewrite decode response: %w", err)
	}
	if decoded.Error != nil && decoded.Error.Message != "" {
		return empty, fmt.Errorf("query rewrite upstream error: %s", decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return empty, errors.New("query rewrite empty choices")
	}
	content := extractJSONObject(decoded.Choices[0].Message.Content)
	if content == "" {
		return empty, errors.New("query rewrite no json object in content")
	}
	var parsed RewriteResult
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return empty, fmt.Errorf("query rewrite parse content: %w", err)
	}
	result := RewriteResult{
		RewrittenEN: strings.TrimSpace(parsed.RewrittenEN),
		Keywords:    normalizeTerms(parsed.Keywords),
		Subqueries:  normalizeTerms(parsed.Subqueries),
	}
	if result.RewrittenEN == "" && len(result.Keywords) == 0 && len(result.Subqueries) == 0 {
		return empty, errors.New("query rewrite produced nothing usable")
	}
	return result, nil
}

// Variants 返回这一轮要分别检索的查询列表，**第一个元素永远是原始查询**。
// 改写关闭、未触发、超时或报错时只返回原始查询，调用方据此退化为单路检索。
func (r *QueryRewriter) Variants(ctx context.Context, query string) []string {
	original := strings.TrimSpace(query)
	if original == "" {
		return nil
	}
	variants := []string{original}
	if r == nil {
		return variants
	}
	result, err := r.Rewrite(ctx, query)
	if err != nil {
		if !errors.Is(err, ErrRewriteSkipped) {
			// 改写失败不是检索失败：记一条日志后按原始查询继续。
			logger.Warn("query rewrite degraded to original query: %v", err)
		}
		return variants
	}
	extras := make([]string, 0, len(result.Subqueries)+2)
	if result.RewrittenEN != "" {
		extras = append(extras, result.RewrittenEN)
	}
	if len(result.Keywords) > 0 {
		extras = append(extras, strings.Join(result.Keywords, " "))
	}
	extras = append(extras, result.Subqueries...)
	return appendVariants(variants, extras, r.maxSubqueries())
}

// appendVariants 把 extras 去重后追加到 base 上，最多追加 limit 条。
func appendVariants(base []string, extras []string, limit int) []string {
	seen := make(map[string]bool, len(base)+len(extras))
	for _, item := range base {
		seen[strings.ToLower(strings.TrimSpace(item))] = true
	}
	added := 0
	for _, item := range extras {
		if limit > 0 && added >= limit {
			break
		}
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if seen[key] {
			continue
		}
		seen[key] = true
		base = append(base, trimmed)
		added++
	}
	return base
}

// MergeRRF 用 Reciprocal Rank Fusion 融合多路检索结果，并按融合分排序后截断到 k 条。
// 同一 chunk 在多路里出现会累加分数，因此"多路都召回"的结果自然排到前面。
// k <= 0 表示不截断。
func MergeRRF(sets [][]*schema.Document, k int) []*schema.Document {
	type entry struct {
		doc   *schema.Document
		score float64
		order int
	}
	merged := make(map[string]*entry)
	order := 0
	for _, set := range sets {
		for rank, doc := range set {
			if doc == nil {
				continue
			}
			key := documentKey(doc)
			item, ok := merged[key]
			if !ok {
				item = &entry{doc: doc, order: order}
				merged[key] = item
				order++
			}
			item.score += 1.0 / float64(rrfK+rank+1)
		}
	}
	entries := make([]*entry, 0, len(merged))
	for _, item := range merged {
		entries = append(entries, item)
	}
	// 分数相同（例如只有一路召回）时按首次出现顺序稳定排列，保证结果可复现。
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].score != entries[j].score {
			return entries[i].score > entries[j].score
		}
		return entries[i].order < entries[j].order
	})
	docs := make([]*schema.Document, 0, len(entries))
	for _, item := range entries {
		docs = append(docs, item.doc)
	}
	if k > 0 && len(docs) > k {
		docs = docs[:k]
	}
	return docs
}

// documentKey 给一个 chunk 生成跨路去重的稳定键。
// 优先用 Milvus 返回的主键；没有主键时退回 metadata 里的定位信息，最后才用内容哈希。
func documentKey(doc *schema.Document) string {
	if doc.ID != "" {
		return doc.ID
	}
	if doc.MetaData != nil {
		path := strings.TrimSpace(fmt.Sprintf("%v", doc.MetaData["source_path"]))
		if path != "" && path != "<nil>" {
			line := strings.TrimSpace(fmt.Sprintf("%v", doc.MetaData["line_start"]))
			return path + "#" + line
		}
	}
	sum := sha1.Sum([]byte(doc.Content))
	return "sha1:" + hex.EncodeToString(sum[:])
}

// extractJSONObject 从模型输出里取出第一个 JSON 对象（容忍 ```json 代码块与前后寒暄）。
func extractJSONObject(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "```") {
		if idx := strings.Index(trimmed, "\n"); idx >= 0 {
			trimmed = trimmed[idx+1:]
		}
		trimmed = strings.TrimSuffix(strings.TrimSpace(trimmed), "```")
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end <= start {
		return ""
	}
	return strings.TrimSpace(trimmed[start : end+1])
}

// normalizeTerms 清洗 LLM 返回的词条列表：去空白、丢空串、大小写无关去重。
func normalizeTerms(items []string) []string {
	out := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, trimmed)
	}
	return out
}

// firstNonEmpty 返回第一个非空白字符串，用于改写配置对顶层 deepseek 配置的兜底。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// truncateRunes 按 rune 截断字符串，避免错误信息里回显超长响应体。
func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}
