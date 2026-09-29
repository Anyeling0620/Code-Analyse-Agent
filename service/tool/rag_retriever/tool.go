package rag_retriever

import (
	"context"
	"fmt"
	"strings"

	"edu.agent.code/adaptor/vector"
	"edu.agent.code/service/rag"
	"edu.agent.code/utils/logger"
	vectorRetriver "github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
	"github.com/gogf/gf/v2/util/gconv"
)

const (
	ToolName        = "rag_retriever"
	defaultTopK     = 20
	maxContextRunes = 8000
)

type Input struct {
	Query string `json:"query" jsonschema:"required,description=检索关键词或问题。应提炼用户问题中的核心概念、术语或意图，而不是直接复制原始问题。如果首次检索无结果，尝试换同义词或更泛化的关键词。"`
	TopK  int    `json:"top_k,omitempty" jsonschema:"description=返回的文档片段数量，默认 5，最大 10。信息需求较大时可适当调高。"`
}

// Option 用于注入可选依赖，保持 NewTool 的既有调用方式（不传 option 时行为不变）。
type Option func(*options)

type options struct {
	rewriter *rag.QueryRewriter
}

// WithQueryRewriter 挂载中文查询改写：开启后，中文自然语言 query 会额外补一路英文检索式
// 与子问题并行召回，再用 RRF 融合。改写在关闭/超时/报错时自动退回原始单路检索。
func WithQueryRewriter(rewriter *rag.QueryRewriter) Option {
	return func(o *options) {
		o.rewriter = rewriter
	}
}

func NewTool(store vector.IStore, opts ...Option) (tool.BaseTool, error) {
	opt := &options{}
	for _, apply := range opts {
		if apply != nil {
			apply(opt)
		}
	}
	return toolutils.InferTool(
		ToolName,
		"从本地知识库检索相关内容。当用户提问涉及Go语言相关知识，课程知识、文档资料、配置说明、概念解释、操作指引等，且当前对话上下文中找不到足够信息时使用。如果首次检索结果不理想或为空，可以用不同的关键词再次调用。",
		func(ctx context.Context, input Input) (string, error) {
			return retriever(ctx, store, opt.rewriter, input)
		},
	)
}

func retriever(ctx context.Context, store vector.IStore, rewriter *rag.QueryRewriter, input Input) (string, error) {
	if store == nil {
		return "rag 检索不可用", nil
	}
	if input.Query == "" {
		return "检索关键词不能为空", nil
	}
	topK := input.TopK
	if topK <= 0 {
		topK = defaultTopK
	}
	docs, err := retrieve(ctx, store, rewriter, input.Query, topK)
	if err != nil {
		return "知识库检索失败: " + err.Error(), nil
	}

	return formatDocuments(docs, maxContextRunes), nil
}

// retrieve 是检索入口：改写不可用或未触发时等价于原来的单路检索。
// 触发改写时，原始查询与改写产物各跑一路，RRF 融合后再截断到 topK。
// 单路失败只跳过该路；全部失败才把错误上抛。
func retrieve(ctx context.Context, store vector.IStore, rewriter *rag.QueryRewriter, query string, topK int) ([]*schema.Document, error) {
	variants := rewriter.Variants(ctx, query)
	if len(variants) <= 1 {
		return store.Retrieve(ctx, query, vectorRetriver.WithTopK(topK))
	}

	sets := make([][]*schema.Document, 0, len(variants))
	var firstErr error
	for _, variant := range variants {
		docs, err := store.Retrieve(ctx, variant, vectorRetriver.WithTopK(topK))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			logger.Warn("rag_retriever 多路召回单路失败，已跳过 query=%q err=%v", variant, err)
			continue
		}
		sets = append(sets, docs)
	}
	if len(sets) == 0 {
		return nil, firstErr
	}
	if len(sets) == 1 {
		return sets[0], nil
	}
	return rag.MergeRRF(sets, topK), nil
}

func formatDocuments(docs []*schema.Document, maxContextRunes int) string {
	if len(docs) == 0 {
		return "没有找到相关文档"
	}
	var b strings.Builder
	b.WriteString("以下是知识库检索到的参考资料，仅作为辅助上下文。若资料与工具返回或代码事实冲突，以工具返回和代码事实为准；引用资料时请标明来源路径。")
	for i, doc := range docs {
		if doc == nil || doc.Content == "" {
			continue
		}
		used := runeLen(b.String())
		if used >= maxContextRunes {
			break
		}
		sourcePath := gconv.String(doc.MetaData["source_path"])
		chunkIndex := gconv.Int(doc.MetaData["chunk_index"])
		heading := gconv.String(doc.MetaData["header"])
		item := fmt.Sprintf("\n\n[%d] source=%s chunk=%d", i+1, sourcePath, chunkIndex)
		if heading != "" {
			item += " heading=" + heading
		}
		item = item + "\n" + doc.Content
		b.WriteString(item)
	}
	return b.String()
}

func runeLen(s string) int {
	return len([]rune(s))
}
