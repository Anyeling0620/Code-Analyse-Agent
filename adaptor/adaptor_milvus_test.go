package adaptor

import (
	"context"
	"edu.agent.code/config"
	"testing"
	"time"
)

// Milvus 不可用时 openMilvusClient 必须在超时内返回错误，而不是永久阻塞。
//
// 这对应一次真实故障：Milvus 容器没起来 → main 卡在 milvusclient.New 内部的
// grpc.DialContext(grpc.WithBlock) → HTTP :8088 从未监听（外部只看到 nginx 502），
// 而进程还活着、systemd 也不会重启它，故障静默了几个小时。
func TestOpenMilvusClientFailsFastWhenMilvusUnavailable(t *testing.T) {
	conf := &config.Config{}
	conf.RAG.Enabled = true
	conf.RAG.Milvus.Address = "127.0.0.1:19531" // 该端口无人监听
	conf.RAG.Milvus.TimeoutSec = 1
	a := &Adaptor{conf: conf}

	start := time.Now()
	err := a.openMilvusClient(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("Milvus 不可用时 openMilvusClient 必须返回错误")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("拨号没有按配置超时返回，耗时 %s——启动会被它卡住", elapsed)
	}
	if ready, statusErr := a.MilvusStatus(); ready || statusErr == nil {
		t.Fatalf("未连上时必须上报未就绪并保留失败原因，实际 ready=%v err=%v", ready, statusErr)
	}
	if a.GetMilvusClient() != nil {
		t.Fatalf("未连上时 GetMilvusClient 必须返回 nil——调用方靠它判空降级")
	}
}

// dial 超时必须有上限：配置里把 timeout_sec 调得再大，也不能把启动重新卡死。
func TestMilvusDialTimeoutIsCapped(t *testing.T) {
	conf := &config.Config{}
	conf.RAG.Enabled = true
	conf.RAG.Milvus.TimeoutSec = 3600
	a := &Adaptor{conf: conf}
	if got := a.milvusDialTimeout(); got > maxMilvusDialTimeoutSec*time.Second {
		t.Fatalf("拨号超时未被上限收敛：%s", got)
	}

	conf.RAG.Milvus.TimeoutSec = 0
	if got := a.milvusDialTimeout(); got != defaultMilvusDialTimeoutSec*time.Second {
		t.Fatalf("未配置 timeout_sec 时应回落到默认值，实际 %s", got)
	}
}

// RAG 未启用时不该要求 Milvus：状态视为正常，客户端为 nil。
func TestMilvusStatusWhenRAGDisabled(t *testing.T) {
	conf := &config.Config{}
	conf.RAG.Enabled = false
	a := &Adaptor{conf: conf}
	if ready, err := a.MilvusStatus(); !ready || err != nil {
		t.Fatalf("RAG 关闭时不该报异常，实际 ready=%v err=%v", ready, err)
	}
	if a.GetMilvusClient() != nil {
		t.Fatalf("RAG 关闭时应返回 nil 客户端")
	}
}
