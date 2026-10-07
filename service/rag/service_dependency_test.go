package rag

import (
	"context"
	"testing"

	"edu.agent.code/common"
	"edu.agent.code/config"
	"edu.agent.code/service/dto"
)

// Milvus 缺位时 NewService 必须以「降级」而不是「失败」返回：进程要能起、HTTP 要能监听。
// 否则 Milvus 一起不来，整个服务（包括与 RAG 无关的功能）都无法上线。
func TestNewServiceDegradesWhenMilvusUnavailable(t *testing.T) {
	conf := &config.Config{RAG: config.RAG{
		Enabled:  true,
		DocsRoot: t.TempDir(),
		Embedding: config.Embedding{
			BaseUrl:    "https://embedding.invalid/v1",
			Model:      "embedding-3",
			Dimensions: 8,
			TimeoutSec: 5,
		},
		Milvus: config.Milvus{Address: "127.0.0.1:19531", Collection: "test_docs"},
	}}
	// fakeAdaptor 的 GetMilvusClient 恒为 nil，等价于 Milvus 没连上。
	svc, err := NewService(context.Background(), &fakeAdaptor{conf: conf})
	if err != nil {
		t.Fatalf("向量库不可用时 NewService 不该返回错误：%v", err)
	}
	if svc == nil {
		t.Fatalf("降级也必须返回可用实例，不能是 nil")
	}
	if svc.store != nil {
		t.Fatalf("未连上 Milvus 时不该有 store")
	}

	// 召回请求按「无结果」处理：不能 panic，也不能把 RAG 故障升级成对话失败。
	chunks, errno := svc.Retriever(context.Background(), &dto.RetrieverReq{Query: "启动流程", TopK: 3})
	if len(chunks) != 0 {
		t.Fatalf("降级时不该有召回结果，实际 %d 条", len(chunks))
	}
	if errno.Code != common.OK.Code {
		t.Fatalf("降级时应返回 OK（空结果），实际 errno=%v", errno)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("降级实例 Close 不该报错：%v", err)
	}
}

// getStore 在向量库不可用时返回明确错误（而不是 nil/panic）；RAG 关闭时同样给出可判定的错误。
func TestGetStoreReportsUnavailable(t *testing.T) {
	conf := &config.Config{RAG: config.RAG{
		Enabled:  true,
		DocsRoot: t.TempDir(),
		Embedding: config.Embedding{
			BaseUrl:    "https://embedding.invalid/v1",
			Model:      "embedding-3",
			Dimensions: 8,
			TimeoutSec: 5,
		},
		Milvus: config.Milvus{Address: "127.0.0.1:19531", Collection: "test_docs"},
	}}
	svc, err := NewService(context.Background(), &fakeAdaptor{conf: conf})
	if err != nil {
		t.Fatalf("NewService 不该失败：%v", err)
	}
	if _, err := svc.getStore(context.Background()); err == nil {
		t.Fatalf("向量库不可用时 getStore 必须返回错误")
	}
	if err := svc.IndexDocs(context.Background()); err == nil {
		t.Fatalf("向量库不可用时 IndexDocs 必须返回错误，而不是静默成功")
	}

	disabled := &Service{conf: config.RAG{Enabled: false}}
	if _, err := disabled.getStore(context.Background()); err == nil {
		t.Fatalf("RAG 关闭时 getStore 必须返回错误")
	}
}
