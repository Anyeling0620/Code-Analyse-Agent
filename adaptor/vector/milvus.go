package vector

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/config"
	"errors"
	"fmt"
	openaiembedding "github.com/cloudwego/eino-ext/components/embedding/openai"
	milvusindexer "github.com/cloudwego/eino-ext/components/indexer/milvus2"
	milvusretriever "github.com/cloudwego/eino-ext/components/retriever/milvus2"
	"github.com/cloudwego/eino-ext/components/retriever/milvus2/search_mode"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"strings"
	"time"
)

const (
	defaultMetadataFiled     = "metadata"
	defaultVectorFiled       = "vector"
	defaultSparseVectorFiled = "sparse_vector"
	defaultMetricType        = "COSINE"

	fieldID      = "id"
	fieldContent = "content"
)

type Milvus struct {
	client     *milvusclient.Client
	collection string
	indexer    *milvusindexer.Indexer
	retriever  *milvusretriever.Retriever
	// TODO 如果知道是哪个模块 可以直接传一个模块 “更快更准？”
}

type MilvusOption struct {
	initCollection bool
}

type NewOption func(opt *MilvusOption)

func WithInitCollection(initCollection bool) NewOption {
	return func(milvus *MilvusOption) {
		milvus.initCollection = initCollection
	}
}
func withDefault(conf config.RAG) config.RAG {
	if conf.TopK == 0 {
		conf.TopK = 5
	}
	if conf.Embedding.TimeoutSec == 0 {
		conf.Embedding.TimeoutSec = 30
	}
	if conf.Milvus.Collection == "" {
		conf.Milvus.Collection = config.ServerFullName
	}
	if conf.Milvus.MetricsType == "" {
		conf.Milvus.MetricsType = defaultMetricType
	}
	return conf
}

func parseMetricType(value string) milvusindexer.MetricType {
	switch strings.ToUpper(value) {
	case "L2":
		return milvusindexer.L2
	case "COSINE":
		return milvusindexer.COSINE
	case "IP":
		return milvusindexer.IP
	default:
		return milvusindexer.COSINE
	}
}

func NewMilvus(ctx context.Context, adaptor adaptor.IAdaptor, opts ...NewOption) (*Milvus, error) {
	conf := withDefault(adaptor.GetConfig().RAG)
	if conf.Embedding.BaseUrl == "" || conf.Embedding.Model == "" || conf.Milvus.Collection == "" {
		return nil, errors.New("embedding dimensions must be provider")
	}
	timeout := time.Duration(conf.Embedding.TimeoutSec) * time.Second
	dimensions := conf.Embedding.Dimensions
	embedder, err := openaiembedding.NewEmbedder(ctx, &openaiembedding.EmbeddingConfig{
		Timeout:    timeout,
		APIKey:     conf.Embedding.APIKey,
		BaseURL:    conf.Embedding.BaseUrl,
		Model:      conf.Embedding.Model,
		Dimensions: &dimensions,
	})
	if err != nil {
		return nil, fmt.Errorf("NewMilvus create embedder failed: %w", err)
	}
	client := adaptor.GetMilvusClient()
	if client == nil {
		return nil, fmt.Errorf("NewMilvus client is nil")
	}
	milvusOpt := &MilvusOption{}
	for _, opt := range opts {
		opt(milvusOpt)
	}
	metricType := parseMetricType(conf.Milvus.MetricsType)
	partition := strings.TrimSpace(conf.Milvus.Partition)
	retrieverConfig := buildRetrieverConfig(client, conf, conf.Milvus.Collection, partition, metricType, embedder)
	newRetriever, err := milvusretriever.NewRetriever(ctx, retrieverConfig)
	if err != nil {
		return nil, fmt.Errorf("NewMilvus create retriever failed: %w", err)
	}
	if !milvusOpt.initCollection {
		// TODO 这里处理召回初始化
		return &Milvus{
			client:     client,
			collection: conf.Milvus.Collection,
			retriever:  newRetriever,
		}, nil
	}
	// 处理索引的逻辑
	if conf.Milvus.DropBeforeIndex {
		ok, err := client.HasCollection(ctx, milvusclient.NewHasCollectionOption(conf.Milvus.Collection))
		if err != nil {
			return nil, fmt.Errorf("NewMilvus check collection failed: %w", err)
		}
		if ok {
			err := client.DropCollection(ctx, milvusclient.NewDropCollectionOption(conf.Milvus.Collection))
			if err != nil {
				return nil, fmt.Errorf("NewMilvus drop collection failed: %w", err)
			}
		}
	}
	indexer, err := milvusindexer.NewIndexer(ctx, buildIndexerConfig(client, conf, conf.Milvus.Collection, partition, metricType, embedder))
	if err != nil {
		return nil, fmt.Errorf("NewMilvus create indexer failed: %w", err)
	}
	m := &Milvus{
		client:     client,
		collection: conf.Milvus.Collection,
		indexer:    indexer,
		// TODO
		retriever: newRetriever,
	}
	return m, nil
}

func partitionList(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}

func buildRetrieverConfig(
	cli *milvusclient.Client,
	conf config.RAG,
	collection, partition string,
	metricType milvusindexer.MetricType,
	embedder *openaiembedding.Embedder) *milvusretriever.RetrieverConfig {
	retrieverConfig := &milvusretriever.RetrieverConfig{
		Client:       cli,
		Collection:   collection,
		Partitions:   partitionList(partition),
		VectorField:  defaultVectorFiled,
		OutputFields: []string{fieldID, fieldContent, defaultMetadataFiled},
		TopK:         conf.TopK,
		SearchMode:   search_mode.NewApproximate(milvusretriever.MetricType(metricType)),
		Embedding:    embedder,
	}
	if conf.Milvus.HybridEnabled {
		retrieverConfig.SparseVectorField = defaultSparseVectorFiled
		retrieverConfig.SearchMode = search_mode.NewHybrid(milvusclient.NewRRFReranker(),
			&search_mode.SubRequest{
				VectorField: defaultVectorFiled,
				MetricType:  milvusretriever.MetricType(metricType),
				TopK:        conf.TopK,
				VectorType:  milvusretriever.DenseVector,
			},
			&search_mode.SubRequest{
				VectorField: defaultSparseVectorFiled,
				MetricType:  milvusretriever.BM25,
				TopK:        conf.TopK,
				VectorType:  milvusretriever.SparseVector,
			},
		)
	}
	return retrieverConfig
}

func buildIndexerConfig(
	cli *milvusclient.Client,
	conf config.RAG,
	collection, partition string,
	metricType milvusindexer.MetricType,
	embedder *openaiembedding.Embedder) *milvusindexer.IndexerConfig {
	indexerConfig := &milvusindexer.IndexerConfig{
		Client:        cli,
		Collection:    collection,
		Description:   "docs knowledge",
		PartitionName: partition,
		Vector: &milvusindexer.VectorConfig{
			Dimension:   int64(conf.Embedding.Dimensions),
			MetricType:  metricType,
			VectorField: defaultVectorFiled,
		},
		Embedding: embedder,
	}
	if conf.Milvus.HybridEnabled {
		indexerConfig.Sparse = &milvusindexer.SparseVectorConfig{
			VectorField: defaultSparseVectorFiled,
			MetricType:  milvusindexer.BM25,
			Method:      milvusindexer.SparseMethodAuto,
		}
		// TODO 怎么去验证他用中文进行稀疏向量后的结果
		indexerConfig.FieldParams = map[string]map[string]string{
			fieldContent: {
				"enable_analyzer": "true",
				"analyzer_params": `{"type": "chinese"}`,
			},
		}
	}
	return indexerConfig
}
