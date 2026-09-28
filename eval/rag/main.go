// Package main 是 "中文直检" vs "中文改写英文再检" 的 A/B 跑分程序。
//
// 它只读 Milvus（绝不 drop/写入/删除集合），把结果写到 eval/rag/results.json。
//
// 用法（在仓库根目录执行）：
//
//	go run ./eval/rag                 # 用 eval/rag/queryset.json 跑全量
//	go run ./eval/rag -smoke          # 用内置 3 条样本先验证五条 arm 都通
//	go run ./eval/rag -schema         # 只打印目标 collection 的字段结构
//	go run ./eval/rag -limit 5        # 只跑前 5 题（调试用）
//
// 五条 arm：
//
//	dense_zh  中文原句 -> 稠密向量（COSINE）检索
//	dense_en  中文原句 -> LLM 改写成英文 -> 稠密向量检索
//	sparse_zh 中文原句 -> BM25 稀疏检索
//	hybrid_zh dense+sparse+RRF+rerank，用中文原句
//	hybrid_en dense+sparse+RRF+rerank，用改写后的英文
//
// 与生产的一致性说明：
//   - 检索参数（字段名 vector/sparse_vector/content/metadata、metric COSINE、
//     BM25、RRF、topK、rerank provider/model）全部取自 agent_code_local.yml 的 rag 段，
//     与 adaptor/vector/milvus.go 的 buildRetrieverConfig 保持一致；
//   - 唯一的有意偏差：生产链路 hybrid 重排后只保留 rerank.top_n=5 条，
//     这里为了能算 Recall@10/@20，重排后保留 topK=20 条（候选池大小与生产相同）。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"edu.agent.code/adaptor/vector"
	"edu.agent.code/config"

	openaiembedding "github.com/cloudwego/eino-ext/components/embedding/openai"
	milvus2 "github.com/cloudwego/eino-ext/components/retriever/milvus2"
	"github.com/cloudwego/eino-ext/components/retriever/milvus2/search_mode"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"gopkg.in/yaml.v3"
)

const (
	defaultCollection = "edu_agent_code_docs_p1c6a6a726a49c9a8"
	defaultQueryset   = "eval/rag/queryset.json"
	defaultOut        = "eval/rag/results.json"
	smokeOut          = "eval/rag/results.smoke.json"
	defaultConfig     = "agent_code_local.yml"

	vectorField       = "vector"
	sparseVectorField = "sparse_vector"
	contentField      = "content"
	metadataField     = "metadata"
	idField           = "id"
)

// hitKs 是报告里使用的 Recall 截断点。
var hitKs = []int{1, 5, 10, 20}

// ---------------------------------------------------------------- 配置读取

type fileConfig struct {
	DeepSeek struct {
		BaseURL string `yaml:"base_url"`
		Model   string `yaml:"model"`
		APIKey  string `yaml:"api_key"`
	} `yaml:"deepseek"`
	RAG struct {
		TopK      int              `yaml:"top_k"`
		Embedding config.Embedding `yaml:"embedding"`
		Milvus    config.Milvus    `yaml:"milvus"`
		Rerank    config.Rerank    `yaml:"rerank"`
	} `yaml:"rag"`
}

func loadFileConfig(path string) (*fileConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	var conf fileConfig
	if err := yaml.Unmarshal(raw, &conf); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	return &conf, nil
}

// ---------------------------------------------------------------- queryset

// Query 是评测集里的一道题。
type Query struct {
	ID               string `json:"id"`
	QuestionZH       string `json:"question_zh"`
	TargetSourcePath string `json:"target_source_path"`
	TargetSymbol     string `json:"target_symbol"`
	TargetKind       string `json:"target_kind"`
	TargetLineStart  int    `json:"target_line_start"`
	Difficulty       string `json:"difficulty"`
}

// Queryset 是 eval/rag/queryset.json 的结构。
type Queryset struct {
	ProjectID   string  `json:"project_id"`
	Collection  string  `json:"collection"`
	CommitSHA   string  `json:"commit_sha"`
	SourceRoot  string  `json:"source_root"`
	GeneratedBy string  `json:"generated_by"`
	GeneratedAt string  `json:"generated_at"`
	Queries     []Query `json:"queries"`
}

func loadQueryset(path string) (*Queryset, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var qs Queryset
	if err := json.Unmarshal(raw, &qs); err != nil {
		return nil, fmt.Errorf("解析 queryset 失败: %w", err)
	}
	return &qs, nil
}

// smokeQueries 是内置的 3 条样本，只用来自证五条 arm 都能跑通。
// 目标都以本仓库（当前 collection 的内容就是本仓库）为准。
func smokeQueries() *Queryset {
	return &Queryset{
		ProjectID:   "p1c6a6a726a49c9a8",
		Collection:  defaultCollection,
		GeneratedBy: "builtin-smoke",
		Queries: []Query{
			{
				ID:               "smoke001",
				QuestionZH:       "向量库的连接是怎么建立的",
				TargetSourcePath: "adaptor/vector/milvus.go",
				TargetSymbol:     "NewMilvus",
				TargetKind:       "func",
				Difficulty:       "descriptive",
			},
			{
				ID:               "smoke002",
				QuestionZH:       "项目专属的向量集合名字是怎么拼出来的",
				TargetSourcePath: "service/rag/project.go",
				TargetSymbol:     "projectCollectionName",
				TargetKind:       "func",
				Difficulty:       "descriptive",
			},
			{
				ID:               "smoke003",
				QuestionZH:       "Go 源码是怎么按顶层符号切分成片段的",
				TargetSourcePath: "service/rag/chunker.go",
				TargetSymbol:     "splitGoSymbols",
				TargetKind:       "func",
				Difficulty:       "explaining",
			},
		},
	}
}

// ---------------------------------------------------------------- LLM 改写

type dsChatClient struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

type dsChatRequest struct {
	Model       string      `json:"model"`
	Messages    []dsMessage `json:"messages"`
	Temperature float64     `json:"temperature"`
	MaxTokens   int         `json:"max_tokens"`
	Stream      bool        `json:"stream"`
}

type dsMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type dsChatResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// rewriteSystemPrompt 要求把中文问题改写成一句英文检索式，且保留原文里的英文标识符。
// 这里不额外注入"必须包含某路径/某符号"之类的提示，避免人为抬高 dense_en。
const rewriteSystemPrompt = "你是代码检索查询改写助手。把用户的中文问题改写成一句英文检索式，" +
	"用于在英文代码库中做语义检索。要求：\n" +
	"1) 只用一句英文自然语言描述要检索的功能点，不要输出多条、不要编号、不要解释；\n" +
	"2) 如果问题里出现了英文标识符（函数名/变量名/文件名），必须原样保留；\n" +
	"3) 不要编造问题中没有的信息；\n" +
	"4) 直接输出这一句英文，不要加引号、不要加前后缀。"

func newDSChatClient(conf *fileConfig) *dsChatClient {
	return &dsChatClient{
		baseURL: strings.TrimRight(conf.DeepSeek.BaseURL, "/"),
		apiKey:  conf.DeepSeek.APIKey,
		model:   conf.DeepSeek.Model,
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

// rewriteToEnglish 把一道中文问题改写成英文检索式。
// deepseek-flash 是推理模型：max_tokens 给小了会出现 content 为空、finish_reason=length，
// 所以这里逐级放大 max_tokens 重试。
func (c *dsChatClient) rewriteToEnglish(ctx context.Context, questionZH string) (string, error) {
	if c == nil || c.baseURL == "" || c.apiKey == "" {
		return "", errors.New("deepseek 配置缺失（base_url / api_key 为空）")
	}
	var lastErr error
	for attempt, maxTokens := range []int{2048, 4096, 8192} {
		body, err := json.Marshal(dsChatRequest{
			Model:       c.model,
			Temperature: 0,
			MaxTokens:   maxTokens,
			Stream:      false,
			Messages: []dsMessage{
				{Role: "system", Content: rewriteSystemPrompt},
				{Role: "user", Content: questionZH},
			},
		})
		if err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		var parsed dsChatResponse
		decErr := json.NewDecoder(resp.Body).Decode(&parsed)
		_ = resp.Body.Close()
		if decErr != nil {
			lastErr = fmt.Errorf("解析改写响应失败: %w", decErr)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			msg := resp.Status
			if parsed.Error != nil && parsed.Error.Message != "" {
				msg = parsed.Error.Message
			}
			lastErr = fmt.Errorf("改写接口 HTTP %s", msg)
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		if len(parsed.Choices) == 0 {
			lastErr = errors.New("改写响应没有 choices")
			continue
		}
		content := sanitizeRewrite(parsed.Choices[0].Message.Content)
		if content == "" {
			lastErr = fmt.Errorf("改写结果为空（finish_reason=%s）", parsed.Choices[0].FinishReason)
			continue
		}
		return content, nil
	}
	return "", lastErr
}

// sanitizeRewrite 去掉模型可能加上的引号、反引号、前缀和多余行，只留第一句。
func sanitizeRewrite(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "`\"'“”‘’")
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		s = s[:idx]
	}
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "Query:")
	s = strings.TrimPrefix(s, "query:")
	s = strings.Trim(s, "`\"'“”‘’")
	return strings.TrimSpace(s)
}

// ---------------------------------------------------------------- 五条 arm

type arm struct {
	name       string
	desc       string
	retriever  *milvus2.Retriever
	reranker   vector.IReranker
	topK       int
	rerankTopN int
	useEnglish bool
}

func newRetriever(
	ctx context.Context,
	client *milvusclient.Client,
	collection string,
	topK int,
	mode milvus2.SearchMode,
	emb embedding.Embedder,
) (*milvus2.Retriever, error) {
	conf := &milvus2.RetrieverConfig{
		Client:            client,
		Collection:        collection,
		VectorField:       vectorField,
		SparseVectorField: sparseVectorField,
		OutputFields:      []string{idField, contentField, metadataField},
		TopK:              topK,
		SearchMode:        mode,
	}
	if emb != nil {
		conf.Embedding = emb
	}
	return milvus2.NewRetriever(ctx, conf)
}

func buildArms(
	ctx context.Context,
	client *milvusclient.Client,
	collection string,
	topK int,
	emb embedding.Embedder,
	reranker vector.IReranker,
) ([]*arm, error) {
	metric := milvus2.COSINE
	denseMode := func() milvus2.SearchMode { return search_mode.NewApproximate(metric) }
	sparseMode := func() milvus2.SearchMode { return search_mode.NewSparse(milvus2.BM25) }
	hybridMode := func() milvus2.SearchMode {
		return search_mode.NewHybrid(milvusclient.NewRRFReranker(),
			&search_mode.SubRequest{
				VectorField: vectorField,
				MetricType:  metric,
				TopK:        topK,
				VectorType:  milvus2.DenseVector,
			},
			&search_mode.SubRequest{
				VectorField: sparseVectorField,
				MetricType:  milvus2.BM25,
				TopK:        topK,
				VectorType:  milvus2.SparseVector,
			},
		)
	}

	specs := []struct {
		name       string
		desc       string
		mode       milvus2.SearchMode
		emb        embedding.Embedder
		reranker   vector.IReranker
		useEnglish bool
	}{
		{"dense_zh", "中文原句 -> 稠密向量（COSINE）TopK", denseMode(), emb, nil, false},
		{"dense_en", "中文改写成英文 -> 稠密向量（COSINE）TopK", denseMode(), emb, nil, true},
		{"sparse_zh", "中文原句 -> BM25 稀疏检索 TopK", sparseMode(), nil, nil, false},
		{"hybrid_zh", "dense+sparse+RRF+rerank，中文原句", hybridMode(), emb, reranker, false},
		{"hybrid_en", "dense+sparse+RRF+rerank，改写英文", hybridMode(), emb, reranker, true},
	}

	arms := make([]*arm, 0, len(specs))
	for _, spec := range specs {
		r, err := newRetriever(ctx, client, collection, topK, spec.mode, spec.emb)
		if err != nil {
			return nil, fmt.Errorf("构建 arm %s 失败: %w", spec.name, err)
		}
		arms = append(arms, &arm{
			name:       spec.name,
			desc:       spec.desc,
			retriever:  r,
			reranker:   spec.reranker,
			topK:       topK,
			rerankTopN: topK,
			useEnglish: spec.useEnglish,
		})
	}
	return arms, nil
}

// search 执行一次检索。重排失败时与生产一致：fail-open 返回未重排结果。
func (a *arm) search(ctx context.Context, query string) ([]*schema.Document, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		docs, err := a.retriever.Retrieve(ctx, query, retriever.WithTopK(a.topK))
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		if a.reranker == nil || len(docs) == 0 {
			return docs, nil
		}
		reranked, rerr := a.reranker.Rerank(ctx, query, docs, a.rerankTopN)
		if rerr != nil {
			return docs, nil
		}
		return reranked, nil
	}
	return nil, lastErr
}

// ---------------------------------------------------------------- 指标

// Hit 是检索结果里的一条命中（只保留评测需要的字段）。
type Hit struct {
	Rank       int     `json:"rank"`
	SourcePath string  `json:"source_path"`
	Symbol     string  `json:"symbol,omitempty"`
	Kind       string  `json:"kind,omitempty"`
	LineStart  int     `json:"line_start,omitempty"`
	Score      float64 `json:"score"`
	HitFile    bool    `json:"hit_file"`
	HitSymbol  bool    `json:"hit_symbol"`
}

// ArmResult 是"某题 + 某 arm"的一次检索明细。
type ArmResult struct {
	Arm           string          `json:"arm"`
	Query         string          `json:"query"`
	LatencyMS     int64           `json:"latency_ms"`
	Error         string          `json:"error,omitempty"`
	Hits          []Hit           `json:"hits"`
	HitFileAt     map[string]bool `json:"hit_file_at"`
	HitSymbolAt   map[string]bool `json:"hit_symbol_at"`
	FirstFileRank int             `json:"first_file_rank"`
	FirstSymbol   int             `json:"first_symbol_rank"`
	MRR           float64         `json:"mrr"`
}

func metaString(doc *schema.Document, key string) string {
	if doc == nil || doc.MetaData == nil {
		return ""
	}
	switch v := doc.MetaData[key].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

func metaInt(doc *schema.Document, key string) int {
	if doc == nil || doc.MetaData == nil {
		return 0
	}
	switch v := doc.MetaData[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i)
		}
	case string:
		var i int
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &i); err == nil {
			return i
		}
	}
	return 0
}

func normalizePath(p string) string {
	return strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(p)), "./")
}

// evalArm 把检索结果折算成指标。symbol 级命中 = metadata.symbol 相等 或 content 里含该符号名。
func evalArm(name, query string, docs []*schema.Document, target Query, latency time.Duration) *ArmResult {
	res := &ArmResult{
		Arm:         name,
		Query:       query,
		LatencyMS:   latency.Milliseconds(),
		Hits:        make([]Hit, 0, len(docs)),
		HitFileAt:   map[string]bool{},
		HitSymbolAt: map[string]bool{},
	}
	wantPath := normalizePath(target.TargetSourcePath)
	wantSymbol := strings.TrimSpace(target.TargetSymbol)

	for i, doc := range docs {
		path := normalizePath(metaString(doc, "source_path"))
		symbol := strings.TrimSpace(metaString(doc, "symbol"))
		hitFile := wantPath != "" && path == wantPath
		hitSymbol := wantSymbol != "" &&
			(symbol == wantSymbol || strings.Contains(doc.Content, wantSymbol))
		res.Hits = append(res.Hits, Hit{
			Rank:       i + 1,
			SourcePath: path,
			Symbol:     symbol,
			Kind:       metaString(doc, "kind"),
			LineStart:  metaInt(doc, "line_start"),
			Score:      doc.Score(),
			HitFile:    hitFile,
			HitSymbol:  hitSymbol,
		})
		if hitFile && res.FirstFileRank == 0 {
			res.FirstFileRank = i + 1
		}
		if hitSymbol && res.FirstSymbol == 0 {
			res.FirstSymbol = i + 1
		}
	}
	if res.FirstFileRank > 0 {
		res.MRR = 1 / float64(res.FirstFileRank)
	}
	for _, k := range hitKs {
		fileHit, symbolHit := false, false
		for _, h := range res.Hits {
			if h.Rank > k {
				break
			}
			fileHit = fileHit || h.HitFile
			symbolHit = symbolHit || h.HitSymbol
		}
		res.HitFileAt[fmt.Sprintf("%d", k)] = fileHit
		res.HitSymbolAt[fmt.Sprintf("%d", k)] = symbolHit
	}
	return res
}

// ---------------------------------------------------------------- 汇总

type ArmSummary struct {
	Arm            string             `json:"arm"`
	Desc           string             `json:"desc"`
	Queries        int                `json:"queries"`
	Errors         int                `json:"errors"`
	FileRecallAt   map[string]float64 `json:"file_recall_at"`
	SymbolRecallAt map[string]float64 `json:"symbol_recall_at"`
	MRR            float64            `json:"mrr"`
	AvgLatencyMS   float64            `json:"avg_latency_ms"`
	AvgRewriteMS   float64            `json:"avg_rewrite_ms,omitempty"`
}

type QueryResult struct {
	Query        Query                 `json:"query"`
	QuestionEN   string                `json:"question_en,omitempty"`
	RewriteMS    int64                 `json:"rewrite_ms"`
	RewriteError string                `json:"rewrite_error,omitempty"`
	Arms         map[string]*ArmResult `json:"arms"`
}

type Results struct {
	GeneratedAt         string                            `json:"generated_at"`
	Collection          string                            `json:"collection"`
	ProjectID           string                            `json:"project_id"`
	QuerysetPath        string                            `json:"queryset_path"`
	CommitSHA           string                            `json:"commit_sha"`
	SourceRoot          string                            `json:"source_root"`
	TopK                int                               `json:"top_k"`
	Arms                []string                          `json:"arms"`
	RewriteModel        string                            `json:"rewrite_model"`
	Queries             int                               `json:"queries"`
	PerQuery            []QueryResult                     `json:"per_query"`
	Summary             map[string]*ArmSummary            `json:"summary"`
	SummaryByDifficulty map[string]map[string]*ArmSummary `json:"summary_by_difficulty"`
	Notes               []string                          `json:"notes"`
}

// summarize 汇总某个子集上各 arm 的指标。
func summarize(arms []*arm, results []QueryResult, subset []int) map[string]*ArmSummary {
	out := make(map[string]*ArmSummary, len(arms))
	for _, a := range arms {
		sum := &ArmSummary{
			Arm:            a.name,
			Desc:           a.desc,
			FileRecallAt:   map[string]float64{},
			SymbolRecallAt: map[string]float64{},
		}
		for _, k := range hitKs {
			sum.FileRecallAt[fmt.Sprintf("%d", k)] = 0
			sum.SymbolRecallAt[fmt.Sprintf("%d", k)] = 0
		}
		var mrr, latTotal, rewriteTotal float64
		n, rewriteN := 0, 0
		for _, idx := range subset {
			qr := results[idx]
			ar, ok := qr.Arms[a.name]
			if !ok {
				continue
			}
			if ar.Error != "" {
				sum.Errors++
				continue
			}
			n++
			latTotal += float64(ar.LatencyMS)
			if a.useEnglish {
				rewriteTotal += float64(qr.RewriteMS)
				rewriteN++
			}
			mrr += ar.MRR
			for _, k := range hitKs {
				key := fmt.Sprintf("%d", k)
				if ar.HitFileAt[key] {
					sum.FileRecallAt[key]++
				}
				if ar.HitSymbolAt[key] {
					sum.SymbolRecallAt[key]++
				}
			}
		}
		sum.Queries = n
		if n > 0 {
			for _, k := range hitKs {
				key := fmt.Sprintf("%d", k)
				sum.FileRecallAt[key] = round4(sum.FileRecallAt[key] / float64(n))
				sum.SymbolRecallAt[key] = round4(sum.SymbolRecallAt[key] / float64(n))
			}
			sum.MRR = round4(mrr / float64(n))
			sum.AvgLatencyMS = round2(latTotal / float64(n))
		}
		if rewriteN > 0 {
			sum.AvgRewriteMS = round2(rewriteTotal / float64(rewriteN))
		}
		out[a.name] = sum
	}
	return out
}

func round4(v float64) float64 { return float64(int(v*10000+0.5)) / 10000 }
func round2(v float64) float64 { return float64(int(v*100+0.5)) / 100 }

// ---------------------------------------------------------------- 输出

func printSummary(res *Results, arms []*arm) {
	fmt.Printf("\n=== 汇总（%d 题，collection=%s）===\n", res.Queries, res.Collection)
	fmt.Printf("%-10s %8s %8s %8s %8s | %8s %8s %8s %8s | %7s %9s %9s\n",
		"arm", "F@1", "F@5", "F@10", "F@20", "S@1", "S@5", "S@10", "S@20", "MRR", "avg_ms", "rewrite_ms")
	for _, a := range arms {
		s := res.Summary[a.name]
		if s == nil {
			continue
		}
		fmt.Printf("%-10s %8.4f %8.4f %8.4f %8.4f | %8.4f %8.4f %8.4f %8.4f | %7.4f %9.1f %9.1f\n",
			a.name,
			s.FileRecallAt["1"], s.FileRecallAt["5"], s.FileRecallAt["10"], s.FileRecallAt["20"],
			s.SymbolRecallAt["1"], s.SymbolRecallAt["5"], s.SymbolRecallAt["10"], s.SymbolRecallAt["20"],
			s.MRR, s.AvgLatencyMS, s.AvgRewriteMS)
	}
	if len(res.SummaryByDifficulty) > 0 {
		diffs := make([]string, 0, len(res.SummaryByDifficulty))
		for d := range res.SummaryByDifficulty {
			diffs = append(diffs, d)
		}
		sort.Strings(diffs)
		fmt.Printf("\n=== 分难度（文件级 Recall@5 / 符号级 Recall@5 / MRR）===\n")
		for _, d := range diffs {
			fmt.Printf("-- difficulty=%s\n", d)
			for _, a := range arms {
				s := res.SummaryByDifficulty[d][a.name]
				if s == nil || s.Queries == 0 {
					continue
				}
				fmt.Printf("   %-10s n=%-3d F@5=%.4f S@5=%.4f mrr=%.4f\n",
					a.name, s.Queries, s.FileRecallAt["5"], s.SymbolRecallAt["5"], s.MRR)
			}
		}
	}
}

func writeJSON(path string, v any) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	return os.WriteFile(path, buf, 0o644)
}

// checkQueryset 用 Milvus 过滤查询逐题核对"目标文件/符号是否真的在 collection 里"。
// 这一步是为了区分"检索没召回"和"目标根本没被索引"——后者对五条 arm 都是不可答的，
// 会把所有 arm 的 Recall 一起压低。
func checkQueryset(ctx context.Context, client *milvusclient.Client, collection string, qs *Queryset) bool {
	fmt.Printf("\n=== 评测集目标核对（collection=%s，%d 题）===\n", collection, len(qs.Queries))
	missingPath, missingSymbol, ok := 0, 0, 0
	for _, q := range qs.Queries {
		wantPath := normalizePath(q.TargetSourcePath)
		expr := fmt.Sprintf("metadata[%q] == %q", "source_path", wantPath)
		opt := milvusclient.NewQueryOption(collection).
			WithFilter(expr).
			WithOutputFields(idField, metadataField).
			WithLimit(500)
		rs, err := client.Query(ctx, opt)
		if err != nil {
			fmt.Printf("  %-8s QUERY-ERR %v\n", q.ID, err)
			missingPath++
			missingSymbol++
			continue
		}
		rows := rs.ResultCount
		symbols := map[string]bool{}
		if metaColumn := rs.GetColumn(metadataField); metaColumn != nil {
			for i := 0; i < rows; i++ {
				raw, gerr := metaColumn.Get(i)
				if gerr != nil {
					continue
				}
				var rawBytes []byte
				switch v := raw.(type) {
				case []byte:
					rawBytes = v
				case string:
					rawBytes = []byte(v)
				default:
					continue
				}
				var m map[string]any
				if json.Unmarshal(rawBytes, &m) != nil {
					continue
				}
				if s, ok := m["symbol"].(string); ok {
					symbols[strings.TrimSpace(s)] = true
				}
			}
		}
		_, hasSymbol := symbols[strings.TrimSpace(q.TargetSymbol)]
		status := "ok"
		switch {
		case rows == 0:
			status = "PATH-MISSING"
			missingPath++
			missingSymbol++
		case q.TargetSymbol != "" && !hasSymbol:
			status = "SYMBOL-MISSING"
			missingSymbol++
		default:
			ok++
		}
		fmt.Printf("  %-8s %-14s chunks=%-3d symbol=%q -> %s\n",
			q.ID, status, rows, q.TargetSymbol, wantPath)
	}
	fmt.Printf("核对结果：完全命中 %d / %d，路径缺失 %d，符号缺失 %d\n",
		ok, len(qs.Queries), missingPath, missingSymbol)
	return missingPath == 0 && missingSymbol == 0
}

// ChunkMeta 是从 collection 里读出来的一条 chunk 元数据（不含正文）。
type ChunkMeta struct {
	ID         string `json:"id"`
	SourcePath string `json:"source_path"`
	Symbol     string `json:"symbol,omitempty"`
	Kind       string `json:"kind,omitempty"`
	LineStart  int    `json:"line_start,omitempty"`
	LineEnd    int    `json:"line_end,omitempty"`
	CommitSHA  string `json:"commit_sha,omitempty"`
	Header     string `json:"header,omitempty"`
}

// CollectionIndex 是 collection 的元数据快照，供评测集挑选/核对目标用。
type CollectionIndex struct {
	Collection string            `json:"collection"`
	RowCount   int               `json:"row_count"`
	CommitSHAs map[string]int    `json:"commit_shas"`
	KindCounts map[string]int    `json:"kind_counts"`
	FileCounts map[string]int    `json:"file_counts"`
	Chunks     []ChunkMeta       `json:"chunks"`
}

// dumpCollection 把 collection 的全部 chunk 元数据导出到 JSON（只读，不写 Milvus）。
func dumpCollection(ctx context.Context, client *milvusclient.Client, collection, out string) error {
	opt := milvusclient.NewQueryOption(collection).
		WithFilter(fmt.Sprintf("%s != \"\"", idField)).
		WithOutputFields(idField, metadataField).
		WithLimit(16384)
	rs, err := client.Query(ctx, opt)
	if err != nil {
		return fmt.Errorf("查询 collection 失败: %w", err)
	}
	idx := &CollectionIndex{
		Collection: collection,
		RowCount:   rs.ResultCount,
		CommitSHAs: map[string]int{},
		KindCounts: map[string]int{},
		FileCounts: map[string]int{},
		Chunks:     make([]ChunkMeta, 0, rs.ResultCount),
	}
	idColumn := rs.GetColumn(idField)
	metaColumn := rs.GetColumn(metadataField)
	for i := 0; i < rs.ResultCount; i++ {
		cm := ChunkMeta{}
		if idColumn != nil {
			if v, err := idColumn.Get(i); err == nil {
				if s, ok := v.(string); ok {
					cm.ID = s
				}
			}
		}
		if metaColumn != nil {
			if v, err := metaColumn.Get(i); err == nil {
				var raw []byte
				switch t := v.(type) {
				case []byte:
					raw = t
				case string:
					raw = []byte(t)
				}
				var m map[string]any
				if len(raw) > 0 && json.Unmarshal(raw, &m) == nil {
					cm.SourcePath = normalizePath(asString(m["source_path"]))
					cm.Symbol = asString(m["symbol"])
					cm.Kind = asString(m["kind"])
					cm.LineStart = asInt(m["line_start"])
					cm.LineEnd = asInt(m["line_end"])
					cm.CommitSHA = asString(m["commit_sha"])
					cm.Header = asString(m["header"])
				}
			}
		}
		if cm.CommitSHA == "" {
			cm.CommitSHA = "(empty)"
		}
		idx.CommitSHAs[cm.CommitSHA]++
		idx.KindCounts[cm.Kind]++
		idx.FileCounts[cm.SourcePath]++
		idx.Chunks = append(idx.Chunks, cm)
	}
	if err := writeJSON(out, idx); err != nil {
		return err
	}
	fmt.Printf("已导出 %d 条 chunk 元数据到 %s\n", rs.ResultCount, out)
	fmt.Printf("commit_sha 分布：%v\n", idx.CommitSHAs)
	fmt.Printf("kind 分布：%v\n", idx.KindCounts)
	return nil
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func asInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		var i int
		if _, err := fmt.Sscanf(strings.TrimSpace(t), "%d", &i); err == nil {
			return i
		}
	}
	return 0
}

// ---------------------------------------------------------------- 兜底出题
//
// 只在 eval/rag/queryset.json 一直不出现时使用：从 collection 里挑真实存在的
// func/method/struct/interface/const/var chunk，让 LLM 写中文问题，并做与评测集
// 同口径的校验（不含符号名、不含路径片段）。产物写 queryset.fallback.json，
// 不会覆盖另一个 agent 的 queryset.json。

type fallbackCandidate struct {
	id      string
	content string
	meta    ChunkMeta
}

const fallbackPrompt = "下面是一段代码仓库里的源码片段。请针对它写一个**自然的中文提问**，" +
	"模拟一个不了解代码的人在提问「这个功能是怎么做的 / 在哪实现 / 长什么样」。要求：\n" +
	"1) 绝对不能出现源码里的英文标识符（函数名、变量名、类型名、常量名），要用中文描述功能；\n" +
	"2) 不能出现文件名、目录名或路径片段；\n" +
	"3) 问题必须是这段代码能回答的，且足够具体、不能是「这个项目是做什么的」这类泛问；\n" +
	"4) 长度 10-40 个汉字，像真人提问；\n" +
	"5) 只输出 JSON，形如 {\"question_zh\":\"...\",\"difficulty\":\"descriptive|locating|explaining\"}，" +
	"不要输出任何其它文字、不要用 markdown 代码块。"

func loadCandidates(ctx context.Context, client *milvusclient.Client, collection string) ([]fallbackCandidate, error) {
	opt := milvusclient.NewQueryOption(collection).
		WithFilter(fmt.Sprintf("%s != \"\"", idField)).
		WithOutputFields(idField, contentField, metadataField).
		WithLimit(16384)
	rs, err := client.Query(ctx, opt)
	if err != nil {
		return nil, err
	}
	idColumn := rs.GetColumn(idField)
	contentColumn := rs.GetColumn(contentField)
	metaColumn := rs.GetColumn(metadataField)
	wantedKind := map[string]bool{
		"func": true, "method": true, "struct": true, "interface": true, "const": true, "var": true,
	}
	out := make([]fallbackCandidate, 0, rs.ResultCount)
	for i := 0; i < rs.ResultCount; i++ {
		cand := fallbackCandidate{}
		if idColumn != nil {
			if v, err := idColumn.Get(i); err == nil {
				cand.id = asString(v)
			}
		}
		if contentColumn != nil {
			if v, err := contentColumn.Get(i); err == nil {
				cand.content = asString(v)
			}
		}
		if metaColumn != nil {
			if v, err := metaColumn.Get(i); err == nil {
				raw := []byte(nil)
				switch t := v.(type) {
				case []byte:
					raw = t
				case string:
					raw = []byte(t)
				}
				var m map[string]any
				if len(raw) > 0 && json.Unmarshal(raw, &m) == nil {
					cand.meta = ChunkMeta{
						SourcePath: normalizePath(asString(m["source_path"])),
						Symbol:     asString(m["symbol"]),
						Kind:       asString(m["kind"]),
						LineStart:  asInt(m["line_start"]),
						LineEnd:    asInt(m["line_end"]),
						CommitSHA:  asString(m["commit_sha"]),
					}
				}
			}
		}
		if !wantedKind[cand.meta.Kind] || strings.TrimSpace(cand.meta.Symbol) == "" {
			continue
		}
		runes := []rune(cand.content)
		if len(runes) < 150 || len(runes) > 4000 {
			continue
		}
		out = append(out, cand)
	}
	return out, nil
}

// questionViolates 检查问题是否泄漏了符号名或路径片段。
func questionViolates(question string, meta ChunkMeta) (bool, string) {
	lower := strings.ToLower(question)
	symbol := strings.TrimSpace(meta.Symbol)
	if symbol != "" {
		if strings.Contains(lower, strings.ToLower(symbol)) {
			return true, "含符号名 " + symbol
		}
		// camelCase / snake_case 拆词
		words := splitIdentWords(symbol)
		for _, w := range words {
			if len([]rune(w)) >= 4 && strings.Contains(lower, strings.ToLower(w)) {
				return true, "含符号拆词 " + w
			}
		}
	}
	for _, seg := range strings.Split(meta.SourcePath, "/") {
		base := strings.TrimSuffix(seg, filepath.Ext(seg))
		if len([]rune(base)) >= 4 && strings.Contains(lower, strings.ToLower(base)) {
			return true, "含路径片段 " + base
		}
	}
	return false, ""
}

// splitIdentWords 把 getUserInfoByPhone / get_user_info 拆成小写词。
func splitIdentWords(s string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = nil
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case r == '_' || r == '-' || r == '.':
			flush()
		case r >= 'A' && r <= 'Z':
			if i > 0 && len(cur) > 0 {
				flush()
			}
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return words
}

func generateFallback(
	ctx context.Context,
	client *milvusclient.Client,
	collection, out string,
	n int,
	ds *dsChatClient,
	sourceRoot string,
) error {
	cands, err := loadCandidates(ctx, client, collection)
	if err != nil {
		return fmt.Errorf("读取候选 chunk 失败: %w", err)
	}
	if len(cands) == 0 {
		return errors.New("没有可用候选 chunk")
	}
	// 按 id 排序保证可复现，然后按文件配额挑选，避免集中在前端/单个文件。
	sort.Slice(cands, func(i, j int) bool { return cands[i].id < cands[j].id })
	const perFileMax = 3
	perFile := map[string]int{}
	selected := make([]fallbackCandidate, 0, n)
	for _, c := range cands {
		if len(selected) >= n {
			break
		}
		if perFile[c.meta.SourcePath] >= perFileMax {
			continue
		}
		perFile[c.meta.SourcePath]++
		selected = append(selected, c)
	}
	if len(selected) < n {
		fmt.Fprintf(os.Stderr, "提示：受每文件最多 %d 条限制，只选到 %d 条候选\n", perFileMax, len(selected))
	}
	fmt.Printf("兜底出题：候选 %d 条，选中 %d 条，开始让 LLM 生成中文问题\n", len(cands), len(selected))

	qs := &Queryset{
		ProjectID:   projectIDFromCollection(collection),
		Collection:  collection,
		CommitSHA:   selected[0].meta.CommitSHA,
		SourceRoot:  sourceRoot,
		GeneratedBy: "eval/rag/main.go -gen（runner 兜底生成，非独立评测集 agent）",
		GeneratedAt: time.Now().Format("2006-01-02"),
		Queries:     []Query{},
	}
	skipped := 0
	for i, c := range selected {
		content := string([]rune(c.content)[:min(len([]rune(c.content)), 3000)])
		answer, err := ds.complete(ctx, fallbackPrompt, content)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  第 %d 条生成失败: %v\n", i+1, err)
			skipped++
			continue
		}
		var parsed struct {
			QuestionZH string `json:"question_zh"`
			Difficulty string `json:"difficulty"`
		}
		if err := json.Unmarshal([]byte(extractJSON(answer)), &parsed); err != nil {
			fmt.Fprintf(os.Stderr, "  第 %d 条 JSON 解析失败: %v（原文：%s）\n", i+1, err, truncate(answer, 120))
			skipped++
			continue
		}
		question := strings.TrimSpace(parsed.QuestionZH)
		if question == "" {
			skipped++
			continue
		}
		if bad, why := questionViolates(question, c.meta); bad {
			fmt.Fprintf(os.Stderr, "  第 %d 条丢弃（%s）：%s\n", i+1, why, question)
			skipped++
			continue
		}
		difficulty := strings.TrimSpace(parsed.Difficulty)
		switch difficulty {
		case "descriptive", "locating", "explaining":
		default:
			if c.meta.Kind == "func" || c.meta.Kind == "method" {
				difficulty = "explaining"
			} else {
				difficulty = "descriptive"
			}
		}
		qs.Queries = append(qs.Queries, Query{
			ID:               fmt.Sprintf("f%03d", len(qs.Queries)+1),
			QuestionZH:       question,
			TargetSourcePath: c.meta.SourcePath,
			TargetSymbol:     c.meta.Symbol,
			TargetKind:       c.meta.Kind,
			TargetLineStart:  c.meta.LineStart,
			Difficulty:       difficulty,
		})
		fmt.Printf("  [%2d/%2d] %-45s <- %s | %s\n", len(qs.Queries), len(selected), truncate(question, 44), c.meta.SourcePath, c.meta.Symbol)
	}
	if len(qs.Queries) == 0 {
		return errors.New("没有生成出任何通过校验的题目")
	}
	if err := writeJSON(out, qs); err != nil {
		return err
	}
	fmt.Printf("兜底评测集已写入 %s（%d 题，丢弃 %d）\n", out, len(qs.Queries), skipped)
	return nil
}

// complete 是改写之外的通用一次问答（兜底出题用），复用同一个 deepseek 客户端。
func (c *dsChatClient) complete(ctx context.Context, systemPrompt, userContent string) (string, error) {
	if c == nil || c.baseURL == "" || c.apiKey == "" {
		return "", errors.New("deepseek 配置缺失")
	}
	var lastErr error
	for attempt, maxTokens := range []int{2048, 4096} {
		body, err := json.Marshal(dsChatRequest{
			Model:       c.model,
			Temperature: 0.3,
			MaxTokens:   maxTokens,
			Messages: []dsMessage{
				{Role: "system", Content: systemPrompt},
				{Role: "user", Content: userContent},
			},
		})
		if err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		var parsed dsChatResponse
		decErr := json.NewDecoder(resp.Body).Decode(&parsed)
		_ = resp.Body.Close()
		if decErr != nil {
			lastErr = decErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %s", resp.Status)
			continue
		}
		if len(parsed.Choices) == 0 {
			lastErr = errors.New("没有 choices")
			continue
		}
		if s := strings.TrimSpace(parsed.Choices[0].Message.Content); s != "" {
			return s, nil
		}
		lastErr = fmt.Errorf("content 为空（finish_reason=%s）", parsed.Choices[0].FinishReason)
	}
	return "", lastErr
}

// extractJSON 从模型输出里抠出第一个 JSON 对象（容忍 markdown 代码块和前后废话）。
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

// projectIDFromCollection 从 collection 名派生 project_id：去掉 "edu_agent_code_docs_" 前缀。
func projectIDFromCollection(collection string) string {
	const prefix = "edu_agent_code_docs_"
	return strings.TrimPrefix(collection, prefix)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// ---------------------------------------------------------------- main

func main() {
	configPath := flag.String("config", defaultConfig, "配置文件（agent_code_local.yml）")
	collection := flag.String("collection", defaultCollection, "目标 Milvus collection")
	querysetPath := flag.String("queryset", defaultQueryset, "评测集 JSON")
	outPath := flag.String("out", defaultOut, "结果输出 JSON")
	topK := flag.Int("topk", 20, "每条 arm 的 topK（0 表示取配置文件里的 rag.top_k）")
	limit := flag.Int("limit", 0, "只跑前 N 题（0 表示全部）")
	smoke := flag.Bool("smoke", false, "用内置 3 条样本跑通五条 arm")
	schemaOnly := flag.Bool("schema", false, "只打印目标 collection 的字段结构后退出")
	checkOnly := flag.Bool("check", false, "只核对评测集里的目标文件/符号是否在 collection 里，然后退出")
	dump := flag.Bool("dump", false, "导出 collection 的全部 chunk 元数据（只读）后退出")
	dumpOut := flag.String("dump-out", "eval/rag/collection_index.json", "chunk 元数据导出路径")
	gen := flag.Int("gen", 0, "兜底出题：从 collection 里生成 N 条中文问题（写 queryset.fallback.json）")
	genOut := flag.String("gen-out", "eval/rag/queryset.fallback.json", "兜底评测集输出路径")
	rewriteWorkers := flag.Int("rewrite-workers", 4, "改写并发数")
	flag.Parse()

	ctx := context.Background()

	conf, err := loadFileConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置读取失败: %v\n", err)
		os.Exit(1)
	}
	if *topK <= 0 {
		*topK = conf.RAG.TopK
		if *topK <= 0 {
			*topK = 20
		}
	}
	if *smoke && *outPath == defaultOut {
		*outPath = smokeOut
	}

	client, err := milvusclient.New(ctx, &milvusclient.ClientConfig{
		Address:  conf.RAG.Milvus.Address,
		Username: conf.RAG.Milvus.UserName,
		Password: conf.RAG.Milvus.Password,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "连接 Milvus 失败: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = client.Close(ctx) }()

	// 目标 collection 的字段结构：先自证存在且字段名与检索配置一致。
	coll, err := client.DescribeCollection(ctx, milvusclient.NewDescribeCollectionOption(*collection))
	if err != nil {
		fmt.Fprintf(os.Stderr, "collection %s 不可用: %v\n", *collection, err)
		os.Exit(2)
	}
	fmt.Printf("collection=%s loaded=%v consistency=%v\n", coll.Name, coll.Loaded, coll.ConsistencyLevel)
	if coll.Schema != nil {
		for _, f := range coll.Schema.Fields {
			fmt.Printf("  field %-16s type=%v pk=%v params=%v\n", f.Name, f.DataType, f.PrimaryKey, f.TypeParams)
		}
		for _, fn := range coll.Schema.Functions {
			fmt.Printf("  function %s\n", fn.Name)
		}
	}
	if *schemaOnly {
		if stats, serr := client.GetCollectionStats(ctx, milvusclient.NewGetCollectionStatsOption(*collection)); serr == nil {
			fmt.Printf("stats=%v（row_count 可能延迟，以实际 query 为准）\n", stats)
		}
		return
	}

	if *checkOnly {
		qs := smokeQueries()
		if !*smoke {
			loaded, err := loadQueryset(*querysetPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "读取 queryset 失败（%s）: %v\n", *querysetPath, err)
				os.Exit(3)
			}
			qs = loaded
		}
		if !checkQueryset(ctx, client, *collection, qs) {
			os.Exit(4)
		}
		return
	}

	if *dump {
		if err := dumpCollection(ctx, client, *collection, *dumpOut); err != nil {
			fmt.Fprintf(os.Stderr, "导出失败: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *gen > 0 {
		sourceRoot := ""
		if qs, err := loadQueryset(*querysetPath); err == nil {
			sourceRoot = qs.SourceRoot
		}
		if err := generateFallback(ctx, client, *collection, *genOut, *gen, newDSChatClient(conf), sourceRoot); err != nil {
			fmt.Fprintf(os.Stderr, "兜底出题失败: %v\n", err)
			os.Exit(1)
		}
		return
	}

	dim := conf.RAG.Embedding.Dimensions
	embTimeout := time.Duration(conf.RAG.Embedding.TimeoutSec) * time.Second
	if embTimeout <= 0 {
		embTimeout = 60 * time.Second
	}
	embedder, err := openaiembedding.NewEmbedder(ctx, &openaiembedding.EmbeddingConfig{
		Timeout:    embTimeout,
		APIKey:     conf.RAG.Embedding.APIKey,
		BaseURL:    conf.RAG.Embedding.BaseUrl,
		Model:      conf.RAG.Embedding.Model,
		Dimensions: &dim,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "构建 embedding 客户端失败: %v\n", err)
		os.Exit(1)
	}
	reranker := vector.NewReranker(conf.RAG.Rerank)
	if reranker == nil {
		fmt.Fprintln(os.Stderr, "警告：rerank 未启用/配置不完整，hybrid 两路将退化为无重排的 RRF 结果")
	}

	arms, err := buildArms(ctx, client, *collection, *topK, embedder, reranker)
	if err != nil {
		fmt.Fprintf(os.Stderr, "构建检索 arm 失败: %v\n", err)
		os.Exit(1)
	}

	qs := smokeQueries()
	if !*smoke {
		loaded, err := loadQueryset(*querysetPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "读取 queryset 失败（%s）: %v\n", *querysetPath, err)
			os.Exit(3)
		}
		qs = loaded
		qs.Collection = *collection
	}
	if len(qs.Queries) == 0 {
		fmt.Fprintln(os.Stderr, "queryset 里没有题目")
		os.Exit(3)
	}
	if *limit > 0 && *limit < len(qs.Queries) {
		qs.Queries = qs.Queries[:*limit]
	}

	fmt.Printf("题目数=%d topK=%d rewrite_model=%s\n", len(qs.Queries), *topK, conf.DeepSeek.Model)

	// 1) 先把所有题目的英文改写跑完（并发），失败不中断，只记录。
	rewrites := make([]string, len(qs.Queries))
	rewriteMS := make([]int64, len(qs.Queries))
	rewriteErr := make([]string, len(qs.Queries))
	ds := newDSChatClient(conf)
	{
		jobs := make(chan int, len(qs.Queries))
		var wg sync.WaitGroup
		workers := *rewriteWorkers
		if workers < 1 {
			workers = 1
		}
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for idx := range jobs {
					started := time.Now()
					en, err := ds.rewriteToEnglish(ctx, qs.Queries[idx].QuestionZH)
					rewriteMS[idx] = time.Since(started).Milliseconds()
					if err != nil {
						rewriteErr[idx] = err.Error()
						rewrites[idx] = qs.Queries[idx].QuestionZH
						continue
					}
					rewrites[idx] = en
				}
			}()
		}
		for i := range qs.Queries {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
	}
	failedRewrite := 0
	for _, e := range rewriteErr {
		if e != "" {
			failedRewrite++
		}
	}
	fmt.Printf("改写完成：成功=%d 失败=%d\n", len(qs.Queries)-failedRewrite, failedRewrite)

	// 2) 逐题跑五条 arm。
	results := &Results{
		GeneratedAt:  time.Now().Format(time.RFC3339),
		Collection:   *collection,
		ProjectID:    qs.ProjectID,
		QuerysetPath: *querysetPath,
		CommitSHA:    qs.CommitSHA,
		SourceRoot:   qs.SourceRoot,
		TopK:         *topK,
		RewriteModel: conf.DeepSeek.Model,
		Queries:      len(qs.Queries),
		Arms:         []string{},
		Notes: []string{
			"hybrid 两路在重排后保留 topK 条（生产只保留 rerank.top_n=5）；候选池大小与生产一致。",
			"检索参数取自 agent_code_local.yml 的 rag 段，字段名/度量/BM25/RRF 与 adaptor/vector/milvus.go 一致。",
			"dense 两路只走稠密向量（COSINE），不走 BM25；sparse_zh 只走 BM25。",
			"symbol 级命中判定：metadata.symbol 等于目标符号，或 content 里包含该符号名。",
		},
	}
	for _, a := range arms {
		results.Arms = append(results.Arms, a.name)
	}

	for i, q := range qs.Queries {
		qr := QueryResult{
			Query:        q,
			QuestionEN:   rewrites[i],
			RewriteMS:    rewriteMS[i],
			RewriteError: rewriteErr[i],
			Arms:         map[string]*ArmResult{},
		}
		for _, a := range arms {
			queryText := q.QuestionZH
			if a.useEnglish {
				queryText = rewrites[i]
			}
			started := time.Now()
			docs, err := a.search(ctx, queryText)
			elapsed := time.Since(started)
			if err != nil {
				qr.Arms[a.name] = &ArmResult{
					Arm:         a.name,
					Query:       queryText,
					LatencyMS:   elapsed.Milliseconds(),
					Error:       err.Error(),
					Hits:        []Hit{},
					HitFileAt:   map[string]bool{},
					HitSymbolAt: map[string]bool{},
				}
				continue
			}
			qr.Arms[a.name] = evalArm(a.name, queryText, docs, q, elapsed)
		}
		results.PerQuery = append(results.PerQuery, qr)

		mark := func(armName string) string {
			ar := qr.Arms[armName]
			if ar == nil || ar.Error != "" {
				return "ERR"
			}
			if ar.HitSymbolAt["10"] {
				return "sym"
			}
			if ar.HitFileAt["10"] {
				return "file"
			}
			return "miss"
		}
		fmt.Printf("[%2d/%2d] %s zh@10=%-4s en@10=%-4s en=%q\n",
			i+1, len(qs.Queries), q.ID, mark("dense_zh"), mark("dense_en"), truncate(rewrites[i], 70))
	}

	// 3) 汇总（全量 + 分难度）。
	all := make([]int, 0, len(results.PerQuery))
	byDifficulty := map[string][]int{}
	for i, qr := range results.PerQuery {
		all = append(all, i)
		d := qr.Query.Difficulty
		if d == "" {
			d = "unknown"
		}
		byDifficulty[d] = append(byDifficulty[d], i)
	}
	results.Summary = summarize(arms, results.PerQuery, all)
	results.SummaryByDifficulty = map[string]map[string]*ArmSummary{}
	for d, idxs := range byDifficulty {
		results.SummaryByDifficulty[d] = summarize(arms, results.PerQuery, idxs)
	}

	if err := writeJSON(*outPath, results); err != nil {
		fmt.Fprintf(os.Stderr, "写入结果失败: %v\n", err)
		os.Exit(1)
	}
	printSummary(results, arms)
	fmt.Printf("\n结果已写入 %s\n", *outPath)
}
