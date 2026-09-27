package rag_retriever

import (
	"context"
	"fmt"
	"strings"

	"edu.agent.code/adaptor/vector"
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

func NewTool(store vector.IStore) (tool.BaseTool, error) {
	return toolutils.InferTool(
		ToolName,
		"从本地知识库检索相关内容。当用户提问涉及Go语言相关知识，课程知识、文档资料、配置说明、概念解释、操作指引等，且当前对话上下文中找不到足够信息时使用。如果首次检索结果不理想或为空，可以用不同的关键词再次调用。",
		func(ctx context.Context, input Input) (string, error) {
			return retriever(ctx, store, input)
		},
	)
}

func retriever(ctx context.Context, store vector.IStore, input Input) (string, error) {
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
	docs, err := store.Retrieve(ctx, input.Query, vectorRetriver.WithTopK(topK))
	if err != nil {
		return "知识库检索失败: " + err.Error(), nil
	}

	return formatDocuments(docs, maxContextRunes), nil
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
