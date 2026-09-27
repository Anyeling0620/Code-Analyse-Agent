package vector

import (
	"context"
	"github.com/cloudwego/eino/components/retriever"
	_ "github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
)

type IStore interface {
	// Store 写入分块的Eino Document，返回向量数据库保存的文档块ID
	Store(ctx context.Context, docs []*schema.Document) ([]string, error)
	// Retrieve 通过自然语言召回TopK的文档块，opts可选一些其他参数
	Retrieve(ctx context.Context, query string, opts ...retriever.Option) ([]*schema.Document, error)
	// Close 释放底层连接
	Close() error
	// Health 健康检查 确认向量数据库是否正常
	Health(ctx context.Context) error
}
