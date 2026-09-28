package vector

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/config"
	"edu.agent.code/utils/logger"
	"errors"
	"fmt"
	openaiembedding "github.com/cloudwego/eino-ext/components/embedding/openai"
	milvusindexer "github.com/cloudwego/eino-ext/components/indexer/milvus2"
	milvusretriever "github.com/cloudwego/eino-ext/components/retriever/milvus2"
	"github.com/cloudwego/eino-ext/components/retriever/milvus2/search_mode"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"go.uber.org/zap"
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
	conf       config.RAG
	reranker   IReranker
	topN       int
	// TODO 如果知道是哪个模块 可以直接传一个模块 “更快更准？”
}

type MilvusOption struct {
	initCollection bool
	reranker       IReranker
}

type NewOption func(opt *MilvusOption)

func WithInitCollection(initCollection bool) NewOption {
	return func(milvus *MilvusOption) {
		milvus.initCollection = initCollection
	}
}

func WithReranker(reranker IReranker) NewOption {
	return func(o *MilvusOption) {
		o.reranker = reranker
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
	if milvusOpt.reranker != nil && conf.Rerank.TopN > 0 {
		conf.TopK = max(conf.TopK, conf.Rerank.TopN)
	}
	metricType := parseMetricType(conf.Milvus.MetricsType)
	partition := strings.TrimSpace(conf.Milvus.Partition)
	retrieverConfig := buildRetrieverConfig(client, conf, conf.Milvus.Collection, partition, metricType, embedder)

	if !milvusOpt.initCollection {
		// TODO 这里处理召回初始化
		newRetriever, err := milvusretriever.NewRetriever(ctx, retrieverConfig)
		if err != nil {
			return nil, fmt.Errorf("NewMilvus create retriever failed: %w", err)
		}
		return &Milvus{
			client:     client,
			collection: conf.Milvus.Collection,
			retriever:  newRetriever,
			reranker:   milvusOpt.reranker,
			topN:       conf.Rerank.TopN,
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
	newRetriever, err := milvusretriever.NewRetriever(ctx, retrieverConfig)
	if err != nil {
		return nil, fmt.Errorf("NewMilvus index create retriever failed: %w", err)
	}
	m := &Milvus{
		client:     client,
		collection: conf.Milvus.Collection,
		indexer:    indexer,
		retriever:  newRetriever,
		conf:       conf,
		reranker:   milvusOpt.reranker,
		topN:       conf.Rerank.TopN,
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
		// analyzer_params 选择 chinese（jieba）而非 standard，是实测对比后的结论：
		//   1) chinese 不会把代码标识符切碎——查询 getUserInfoByPhone 能命中含有该标识符的文档，
		//      而查询 phone 只命中把它当独立单词的文档，说明整标识符被当作一个 token 保留；
		//   2) 中文查询只有 chinese 能召回——查询"鉴权"在 chinese 下命中，standard 会把 CJK
		//      逐字切分，导致完全召不回；
		//   3) 两者都做不到 camelCase 子词匹配：查询 getUserInfo 都召不回 getUserInfoByPhone，
		//      这部分语义目前依赖 dense 向量兜底。
		// 因此这里保持 chinese；若要支持子词匹配，需要另外引入标识符展开/自定义 tokenizer，
		// 不能靠换成 standard 解决。
		// 注意：analyzer 是建集合时写进 schema 的，改动这里必须重建 collection 才会生效。
		indexerConfig.FieldParams = map[string]map[string]string{
			fieldContent: {
				"enable_analyzer": "true",
				"analyzer_params": `{"type": "chinese"}`,
			},
		}
	}
	return indexerConfig
}

func (m *Milvus) Store(ctx context.Context, docs []*schema.Document) ([]string, error) {
	if m == nil || m.indexer == nil {
		return nil, nil
	}
	ids, err := m.indexer.Store(ctx, docs)
	if err != nil {
		return nil, fmt.Errorf("Milvus Store failed: %w ", err)
	}
	return ids, nil
}

func (m *Milvus) Retrieve(ctx context.Context, query string, opts ...retriever.Option) ([]*schema.Document, error) {
	if m == nil || m.retriever == nil {
		return nil, nil
	}
	docs, err := m.retriever.Retrieve(ctx, query, opts...)
	if err != nil {
		return nil, fmt.Errorf("Milvus_Retrieve: %w", err)
	}
	if m.reranker == nil {
		return docs, nil
	}

	// 召回后的数据重排
	reranked, err := m.reranker.Rerank(ctx, query, docs, m.topN)
	if err != nil {
		logger.Error("Milvus Retrieve Rerank", zap.Error(err), zap.String("query", query), zap.Int("topN", m.topN))
		if m.topN >= len(docs) {
			return docs, nil
		}
		return docs[:m.topN], nil
	}
	return reranked, nil
}
func (m *Milvus) Close() error {
	if m == nil || m.client == nil {
		return nil
	}
	return m.client.Close(context.Background())
}

func (m *Milvus) Health(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if !m.conf.Enabled {
		return nil
	}
	if m.client == nil {
		return fmt.Errorf("Milvus_Health: client is nil")
	}
	_, err := m.client.HasCollection(ctx, milvusclient.NewHasCollectionOption(m.collection))
	if err != nil {
		return fmt.Errorf("Milvus_Health: %w", err)
	}
	return nil
}
