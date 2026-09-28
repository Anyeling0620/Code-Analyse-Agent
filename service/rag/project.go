package rag

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"edu.agent.code/adaptor"
	sessionrepo "edu.agent.code/adaptor/repo/session"
	"edu.agent.code/adaptor/vector"
	"edu.agent.code/common"
	"edu.agent.code/config"
	"edu.agent.code/service/do"
	"edu.agent.code/service/projectid"
	"edu.agent.code/utils/logger"

	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
	"github.com/gogf/gf/v2/util/gconv"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

const (
	// ProjectIDMetadataKey 是 chunk 元数据中项目标识的键名，也是检索侧做二次校验的依据。
	ProjectIDMetadataKey = "project_id"
	// ProjectRootMetadataKey 是 chunk 元数据中项目根目录的键名。
	ProjectRootMetadataKey = "project_root"
	// CommitMetadataKey 是 chunk 元数据中索引时 HEAD commit 的键名。
	CommitMetadataKey = "commit_sha"

	// projectRetrieverToolName 必须与挂到 agent 上的工具名一致，保持对模型而言工具名不变。
	projectRetrieverToolName = "rag_retriever"

	// projectIndexBatchSize 是单次写入 Milvus 的 chunk 数。
	// 受 embedding 提供方单请求 input 上限约束（见 maxEmbeddingBatchSize），不能超过 64。
	projectIndexBatchSize = maxEmbeddingBatchSize
	// projectRetrieverMaxRunes 限制单次工具返回的上下文体量。
	projectRetrieverMaxRunes = 8000
	// projectRetrieverMaxTopK 是模型可以要求的最大片段数。
	projectRetrieverMaxTopK = 20
)

const (
	projectNoProjectTips = "当前会话还没有确定项目，无法使用项目语义检索。" +
		"请先让用户给出项目（git 地址或本机绝对路径），由 repo_fetch 准备好仓库后再重试；" +
		"本轮可以先用 project_scan / project_search / read_files 继续分析。"

	projectIndexNotReadyTips = "当前项目的代码语义索引尚未就绪（状态：%s），暂时无法语义检索。" +
		"请改用 project_search / project_files 精确定位，或稍后重试。"
)

// ProjectRef 描述一次项目级索引请求。
// ProjectID 为空时会用 root（必要时结合 git remote）派生，保证与 repo_fetch 得到同一个标识。
type ProjectRef struct {
	ProjectID string
	Root      string
	Commit    string
}

// IndexStatus 是索引状态机取值。
type IndexStatus string

const (
	StatusNone     IndexStatus = "none"
	StatusIndexing IndexStatus = "indexing"
	StatusReady    IndexStatus = "ready"
	StatusFailed   IndexStatus = "failed"
)

// Status 是某个项目索引状态的快照。
type Status struct {
	ProjectID  string
	Status     IndexStatus
	Commit     string
	ChunkCount int
	IndexedAt  time.Time
	LastError  string
}

// sessionLookup 只依赖会话仓储里"当前项目"这一条读取能力，便于测试替换。
type sessionLookup interface {
	GetByID(ctx context.Context, sessionID string) (*do.SessionContext, error)
}

// ProjectIndexer 按项目维度维护代码仓库索引。
//
// 隔离策略：一个项目一个 Milvus collection（基础名 + "_" + projectID）。
// 写入与召回都只针对该集合，因此不同项目的 chunk 在物理上就不在同一个检索空间里，
// 不需要依赖 metadata 过滤这种"可能写错就失效"的软约束。
type ProjectIndexer struct {
	base     adaptor.IAdaptor
	conf     config.RAG
	sessions sessionLookup

	// storeMu 保护 stores；建集合是一次网络调用，不能和其它项目互相阻塞。
	storeMu sync.Mutex
	stores  map[string]vector.IStore

	// mu 保护下面这些内存状态。
	mu             sync.Mutex
	statuses       map[string]Status
	running        map[string]bool
	rootProject    map[string]string
	sessionProject map[string]ProjectRef
}

// NewProjectIndexer 构造项目级索引器。
// 它不在这里创建 Milvus 实例：集合是按项目惰性创建的，避免启动时对每个项目都建索引。
func NewProjectIndexer(a adaptor.IAdaptor) *ProjectIndexer {
	indexer := &ProjectIndexer{
		base:           a,
		stores:         make(map[string]vector.IStore),
		statuses:       make(map[string]Status),
		running:        make(map[string]bool),
		rootProject:    make(map[string]string),
		sessionProject: make(map[string]ProjectRef),
	}
	if a == nil {
		return indexer
	}
	if conf := a.GetConfig(); conf != nil {
		indexer.conf = withDefault(conf.RAG)
	}
	if a.GetDB() != nil {
		indexer.sessions = sessionrepo.NewSession(a)
	}
	return indexer
}

// EnsureIndexed 幂等地为某个项目建索引：
//  1. 同项目并发调用只跑一个（后面的直接返回，不重复工作）；
//  2. commit 未变且已就绪时毫秒级短路；
//  3. 否则重新取样、切块，先清掉本项目旧块再写入。
//
// 失败会记录到 Status.LastError 并返回错误，由调用方决定是否对用户可见。
func (p *ProjectIndexer) EnsureIndexed(ctx context.Context, ref ProjectRef) error {
	if p == nil || p.base == nil {
		return nil
	}
	// RAG 关闭时索引器是空实现：既不报错也不建集合，保持与 rag.Service 一致。
	if !p.conf.Enabled {
		return nil
	}
	ref = p.normalizeRef(ref)
	if ref.ProjectID == "" || ref.Root == "" {
		return fmt.Errorf("project rag ensure indexed: project_id 与 root 不能为空")
	}
	p.remember(ctx, ref)

	if !p.acquire(ref.ProjectID) {
		logger.Info("project rag index already running, skip project_id=%s", ref.ProjectID)
		return nil
	}
	defer p.release(ref.ProjectID)

	if previous, ok := p.snapshot(ref.ProjectID); ok &&
		previous.Status == StatusReady && ref.Commit != "" && previous.Commit == ref.Commit {
		return nil
	}

	p.setStatus(Status{ProjectID: ref.ProjectID, Status: StatusIndexing, Commit: ref.Commit})
	startedAt := time.Now()
	chunkCount, err := p.indexProject(ctx, ref)
	if err != nil {
		p.setStatus(Status{
			ProjectID: ref.ProjectID,
			Status:    StatusFailed,
			Commit:    ref.Commit,
			LastError: err.Error(),
		})
		return err
	}
	p.setStatus(Status{
		ProjectID:  ref.ProjectID,
		Status:     StatusReady,
		Commit:     ref.Commit,
		ChunkCount: chunkCount,
		IndexedAt:  time.Now(),
	})
	logger.Info("project rag index ready project_id=%s chunks=%d elapsed=%s",
		ref.ProjectID, chunkCount, time.Since(startedAt))
	return nil
}

// Retrieve 只在该项目自己的集合内做语义检索，并在返回前用元数据再校验一次，
// 保证调用方拿到的 chunk 一定属于 projectID。
func (p *ProjectIndexer) Retrieve(ctx context.Context, projectID, query string, topK int) ([]*schema.Document, error) {
	if p == nil || p.base == nil || !p.conf.Enabled {
		return nil, nil
	}
	projectID = strings.TrimSpace(projectID)
	query = strings.TrimSpace(query)
	if projectID == "" || query == "" {
		return nil, nil
	}
	// 只检索"已经建立过"的集合：未索引过的项目不应因为一次查询就建出空集合。
	store := p.cachedStore(projectID)
	if store == nil {
		// 服务重启后内存里没有该项目的状态，但集合与向量仍然在 Milvus 中，
		// 这里回查一次并重建只读视图，保证"第二轮追问"不依赖进程是否重启过。
		if p.Status(ctx, projectID).Status != StatusReady {
			return nil, nil
		}
		rebuilt, err := p.storeFor(ctx, projectID)
		if err != nil {
			return nil, fmt.Errorf("project rag rebuild store project_id=%s: %w", projectID, err)
		}
		store = rebuilt
	}
	if topK <= 0 {
		topK = p.conf.TopK
	}
	docs, err := store.Retrieve(ctx, query, retriever.WithTopK(topK))
	if err != nil {
		return nil, fmt.Errorf("project rag retrieve project_id=%s: %w", projectID, err)
	}
	return filterByProject(docs, projectID), nil
}

// Status 返回项目索引状态；未登记过的项目返回 StatusNone。
// 内存中没有记录时会回查 Milvus：集合存在即视为已就绪，避免进程重启后误判为"未索引"。
func (p *ProjectIndexer) Status(ctx context.Context, projectID string) Status {
	if p == nil {
		return Status{}
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return Status{Status: StatusNone}
	}
	if status, ok := p.snapshot(projectID); ok {
		return status
	}
	if status, ok := p.rehydrate(ctx, projectID); ok {
		return status
	}
	return Status{ProjectID: projectID, Status: StatusNone}
}

// rehydrate 在内存状态缺失时回查 Milvus：
// 服务重启会清空内存状态，但项目集合与向量都还在，必须能继续被检索到。
// 集合存在即认为可用；chunk 数取 Milvus 的统计值（该值可能延迟更新，仅作展示用）。
func (p *ProjectIndexer) rehydrate(ctx context.Context, projectID string) (Status, bool) {
	if p == nil || p.base == nil || !p.conf.Enabled {
		return Status{}, false
	}
	client := p.base.GetMilvusClient()
	if client == nil {
		return Status{}, false
	}
	collection := projectCollectionName(p.conf.Milvus.Collection, projectID)
	exists, err := client.HasCollection(ctx, milvusclient.NewHasCollectionOption(collection))
	if err != nil || !exists {
		return Status{}, false
	}
	status := Status{ProjectID: projectID, Status: StatusReady, IndexedAt: time.Now()}
	if stats, statsErr := client.GetCollectionStats(ctx, milvusclient.NewGetCollectionStatsOption(collection)); statsErr == nil {
		status.ChunkCount = parseRowCount(stats)
		// Milvus 的 row_count 是统计值，刚写入尚未 flush 时可能为 0；
		// 这里保留原始值便于排查，不影响 ready 判定（能检索到才是可用性标准）。
		logger.Debug("project rag collection stats collection=%s stats=%v", collection, stats)
	}
	p.setStatus(status)
	logger.Info("project rag rehydrated from milvus project_id=%s collection=%s chunks=%d",
		projectID, collection, status.ChunkCount)
	return status, true
}

// parseRowCount 从 Milvus 集合统计里取行数；该值是统计信息，可能延迟，取不到就返回 0。
func parseRowCount(stats map[string]string) int {
	for _, key := range []string{"row_count", "RowCount", "rowCount"} {
		raw, ok := stats[key]
		if !ok {
			continue
		}
		if value, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			return value
		}
	}
	return 0
}

// Tool 返回项目级 rag_retriever 工具：每次调用都从会话上下文解析"当前项目"，
// 索引未就绪时返回可读的降级提示而不是错误，让模型自然退回 project_search。
// RAG 关闭时返回 nil，由调用方决定不挂载该工具。
func (p *ProjectIndexer) Tool(_ context.Context) (tool.BaseTool, error) {
	if p == nil || p.base == nil || !p.conf.Enabled {
		return nil, nil
	}
	return toolutils.InferTool(
		projectRetrieverToolName,
		"语义检索当前已分析项目（代码/配置/文档）的内容，用于回答「在哪实现、怎么串起来」这类问题："+
			"跨文件的鉴权、路由注册、配置加载、数据模型、前后端协议等。"+
			"适合「不知道确切关键词」的探索；确切知道符号名时用 project_search 更准，需要准确原文时用 read_files。",
		func(ctx context.Context, input projectRetrieverInput) (string, error) {
			return p.retrieveForTool(ctx, input)
		},
	)
}

type projectRetrieverInput struct {
	Query string `json:"query" jsonschema:"required,description=用自然语言描述要找什么，例如“鉴权是在哪一层做的”“路由是怎么注册的”。不必猜代码里的符号名。"`
	TopK  int    `json:"top_k,omitempty" jsonschema:"description=返回的代码片段数量，默认 8，最大 20。"`
}

func (p *ProjectIndexer) retrieveForTool(ctx context.Context, input projectRetrieverInput) (string, error) {
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return "检索关键词不能为空。", nil
	}
	ref, ok := p.resolveCurrent(ctx)
	if !ok {
		return projectNoProjectTips, nil
	}
	if status := p.Status(ctx, ref.ProjectID); status.Status != StatusReady {
		return fmt.Sprintf(projectIndexNotReadyTips, describeIndexStatus(status)), nil
	}
	topK := input.TopK
	if topK <= 0 {
		topK = p.conf.TopK
	}
	if topK > projectRetrieverMaxTopK {
		topK = projectRetrieverMaxTopK
	}
	docs, err := p.Retrieve(ctx, ref.ProjectID, query, topK)
	if err != nil {
		logger.Error("project rag retrieve failed project_id=%s err=%s", ref.ProjectID, err.Error())
		return "语义检索失败，请改用 project_search 精确定位。", nil
	}
	if len(docs) == 0 {
		return "没有检索到相关内容。可以换一组更泛化的关键词重试，或用 project_search / read_files 继续定位。", nil
	}
	return formatProjectDocuments(ref.ProjectID, docs), nil
}

// resolveCurrent 解析"当前会话在分析哪个项目"：
//  1. 建索引时按 session 记录过的项目（同一进程内第一轮就能命中）；
//  2. 会话表里的当前项目根目录（跨进程、追问"刚才那个项目"时命中）。
func (p *ProjectIndexer) resolveCurrent(ctx context.Context) (ProjectRef, bool) {
	sessionID := common.SessionIDFromContext(ctx)
	if sessionID == "" {
		return ProjectRef{}, false
	}
	p.mu.Lock()
	ref, ok := p.sessionProject[sessionID]
	p.mu.Unlock()
	if ok && ref.ProjectID != "" {
		return ref, true
	}
	if p.sessions == nil {
		return ProjectRef{}, false
	}
	session, err := p.sessions.GetByID(ctx, sessionID)
	if err != nil || session == nil {
		return ProjectRef{}, false
	}
	ref = p.refFromRoot(session.CurrentProjectRoot)
	if ref.ProjectID == "" {
		return ProjectRef{}, false
	}
	return ref, true
}

// indexProject 完成真正的重活：取样 → 切块 → 清旧块 → 批量写入。
func (p *ProjectIndexer) indexProject(ctx context.Context, ref ProjectRef) (int, error) {
	docs, err := loadDocsInScope(ctx, ref.Root, p.conf, docScope{
		ProjectID:   ref.ProjectID,
		ProjectRoot: ref.Root,
		Commit:      ref.Commit,
	})
	if err != nil {
		return 0, err
	}
	if len(docs) == 0 {
		logger.Warn("project rag no indexable files project_id=%s root=%s", ref.ProjectID, ref.Root)
		return 0, nil
	}
	store, err := p.storeFor(ctx, ref.ProjectID)
	if err != nil {
		return 0, err
	}
	// 同一项目的旧块先清掉：文件内容变化会产生新的 chunk ID，
	// 不清就会出现"旧版本 + 新版本"同时被召回的脏数据。
	p.purge(ctx, ref.ProjectID)

	stored := 0
	for start := 0; start < len(docs); start += projectIndexBatchSize {
		end := start + projectIndexBatchSize
		if end > len(docs) {
			end = len(docs)
		}
		ids, err := store.Store(ctx, docs[start:end])
		if err != nil {
			return stored, fmt.Errorf("project rag store chunks: %w", err)
		}
		stored += len(ids)
	}
	return stored, nil
}

// storeFor 惰性创建（并缓存）某个项目专属集合的读写实例。
func (p *ProjectIndexer) storeFor(ctx context.Context, projectID string) (vector.IStore, error) {
	if store := p.cachedStore(projectID); store != nil {
		return store, nil
	}
	scoped := &collectionAdaptor{
		IAdaptor:   p.base,
		collection: projectCollectionName(p.conf.Milvus.Collection, projectID),
	}
	// initCollection=true：集合不存在时由组件按当前 embedding 维度创建。
	store, err := vector.NewMilvus(ctx, scoped,
		vector.WithInitCollection(true),
		vector.WithReranker(vector.NewReranker(p.conf.Rerank)))
	if err != nil {
		return nil, fmt.Errorf("create project milvus store: %w", err)
	}
	p.storeMu.Lock()
	defer p.storeMu.Unlock()
	if existing, ok := p.stores[projectID]; ok {
		// 并发下已有实例，丢弃本次新建的包装对象。
		// 注意：绝不能对它调用 Close()——底层是共享 Milvus client，关掉会影响其它项目。
		return existing, nil
	}
	p.stores[projectID] = store
	return store, nil
}

func (p *ProjectIndexer) cachedStore(projectID string) vector.IStore {
	p.storeMu.Lock()
	defer p.storeMu.Unlock()
	return p.stores[projectID]
}

// purge 清掉该项目集合内已有的 chunk。集合本就只属于这个项目，因此按 project_id 删除等于清空。
func (p *ProjectIndexer) purge(ctx context.Context, projectID string) {
	client := p.base.GetMilvusClient()
	if client == nil {
		return
	}
	collection := projectCollectionName(p.conf.Milvus.Collection, projectID)
	if _, err := client.Delete(ctx,
		milvusclient.NewDeleteOption(collection).WithExpr(projectFilterExpr(projectID))); err != nil {
		// 删除失败不阻断本次索引：upsert 仍会覆盖同 ID 的 chunk，只是可能残留少量旧块。
		logger.Warn("project rag purge old chunks failed project_id=%s err=%s", projectID, err.Error())
	}
}

// collectionAdaptor 只改写 RAG.Milvus.Collection，让 vector 组件读写"某个项目专属"的集合，
// 其余（DB、共享 Milvus client、embedding/rerank 配置）全部透传。
type collectionAdaptor struct {
	adaptor.IAdaptor
	collection string
}

func (c *collectionAdaptor) GetConfig() *config.Config {
	base := c.IAdaptor.GetConfig()
	if base == nil {
		return nil
	}
	copied := *base
	copied.RAG.Milvus.Collection = c.collection
	return &copied
}

func (p *ProjectIndexer) normalizeRef(ref ProjectRef) ProjectRef {
	ref.ProjectID = strings.TrimSpace(ref.ProjectID)
	ref.Commit = strings.TrimSpace(ref.Commit)
	ref.Root = strings.TrimSpace(ref.Root)
	if ref.Root != "" {
		if abs, err := filepath.Abs(ref.Root); err == nil {
			ref.Root = filepath.Clean(abs)
		}
	}
	if ref.ProjectID == "" {
		ref.ProjectID = p.refFromRoot(ref.Root).ProjectID
	}
	return ref
}

// refFromRoot 从本地根目录反推项目标识：先查本项目进程内已经登记过的映射，
// 再退化为"git remote 优先、否则本地路径"的派生规则（与 repo_fetch 保持一致）。
func (p *ProjectIndexer) refFromRoot(root string) ProjectRef {
	return refFromRootWith(root, p.lookupRootProject)
}

func (p *ProjectIndexer) lookupRootProject(root string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rootProject[normalizeProjectRoot(root)]
}

// remember 记录 root→project_id 与 session→project，供后续检索解析使用。
func (p *ProjectIndexer) remember(ctx context.Context, ref ProjectRef) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ref.Root != "" {
		p.rootProject[normalizeProjectRoot(ref.Root)] = ref.ProjectID
	}
	if sessionID := common.SessionIDFromContext(ctx); sessionID != "" {
		p.sessionProject[sessionID] = ref
	}
}

func (p *ProjectIndexer) acquire(projectID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running[projectID] {
		return false
	}
	p.running[projectID] = true
	return true
}

func (p *ProjectIndexer) release(projectID string) {
	p.mu.Lock()
	delete(p.running, projectID)
	p.mu.Unlock()
}

func (p *ProjectIndexer) snapshot(projectID string) (Status, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	status, ok := p.statuses[projectID]
	return status, ok
}

func (p *ProjectIndexer) setStatus(status Status) {
	p.mu.Lock()
	p.statuses[status.ProjectID] = status
	p.mu.Unlock()
}

// refFromRootWith 允许调用方注入"已知 root→project_id"的查询函数，便于隔离测试。
func refFromRootWith(root string, lookup func(string) string) ProjectRef {
	root = strings.TrimSpace(root)
	if root == "" {
		return ProjectRef{}
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = filepath.Clean(abs)
	}
	if lookup != nil {
		if projectID := strings.TrimSpace(lookup(root)); projectID != "" {
			return ProjectRef{ProjectID: projectID, Root: root}
		}
	}
	return ProjectRef{ProjectID: projectid.Derive(root, gitOriginRemote(root)), Root: root}
}

// normalizeProjectRoot 把根目录归一化成 map key：Windows 下大小写不敏感。
func normalizeProjectRoot(root string) string {
	normalized := filepath.ToSlash(filepath.Clean(strings.TrimSpace(root)))
	normalized = strings.TrimSuffix(normalized, "/")
	if runtime.GOOS == "windows" {
		normalized = strings.ToLower(normalized)
	}
	return normalized
}

// gitOriginRemote 从本地仓库的 .git/config 读取 origin 地址。
// 只读文件、不执行命令：repo_fetch 用 remote 派生项目 ID，检索侧必须拿到同一个 ID 才能命中同一集合。
func gitOriginRemote(root string) string {
	file, err := os.Open(filepath.Join(root, ".git", "config"))
	if err != nil {
		return ""
	}
	defer func() {
		_ = file.Close()
	}()

	inOrigin := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			inOrigin = isOriginSection(line)
			continue
		}
		if !inOrigin {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "url") {
			continue
		}
		return strings.TrimSpace(value)
	}
	return ""
}

// isOriginSection 判断配置行是否为 [remote "origin"]。git 的 worktree/submodule 里 .git 是文件，
// 这种情况下读不到内容，会自然退化为按本地路径派生。
func isOriginSection(line string) bool {
	line = strings.TrimSpace(line)
	if len(line) < 2 || !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
		return false
	}
	inner := strings.TrimSpace(line[1 : len(line)-1])
	if len(inner) < len("remote") || !strings.EqualFold(inner[:len("remote")], "remote") {
		return false
	}
	rest := strings.TrimSpace(inner[len("remote"):])
	rest = strings.Trim(rest, `"'`)
	return strings.EqualFold(rest, "origin")
}

// projectCollectionName 生成项目专属集合名：基础名 + "_" + projectID。
// 两段都做字符清洗，避免配置里出现 Milvus 不接受的字符（例如默认值 edu.agent.code 里的点）。
func projectCollectionName(base, projectID string) string {
	base = sanitizeCollectionName(base)
	if base == "" {
		base = sanitizeCollectionName(config.ServerFullName)
	}
	return base + "_" + sanitizeCollectionName(projectID)
}

func sanitizeCollectionName(name string) string {
	var builder strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	sanitized := strings.Trim(builder.String(), "_")
	if sanitized == "" {
		return ""
	}
	if c := sanitized[0]; c >= '0' && c <= '9' {
		return "c_" + sanitized
	}
	return sanitized
}

// projectFilterExpr 是 Milvus 的 JSON 字段过滤表达式，用于按项目清理/校验。
func projectFilterExpr(projectID string) string {
	return fmt.Sprintf("metadata[%q] == %q", ProjectIDMetadataKey, projectID)
}

// filterByProject 是隔离的兜底校验：即便集合内混入了别的项目数据，也不返回给调用方。
func filterByProject(docs []*schema.Document, projectID string) []*schema.Document {
	filtered := make([]*schema.Document, 0, len(docs))
	for _, doc := range docs {
		if doc == nil || doc.MetaData == nil {
			continue
		}
		if gconv.String(doc.MetaData[ProjectIDMetadataKey]) != projectID {
			continue
		}
		filtered = append(filtered, doc)
	}
	return filtered
}

func describeIndexStatus(status Status) string {
	switch status.Status {
	case StatusNone:
		return "该项目还没有建立索引"
	case StatusIndexing:
		return "正在构建"
	case StatusReady:
		return "已就绪"
	case StatusFailed:
		if status.LastError != "" {
			return "构建失败：" + status.LastError
		}
		return "构建失败"
	default:
		return string(status.Status)
	}
}

func formatProjectDocuments(projectID string, docs []*schema.Document) string {
	var builder strings.Builder
	builder.WriteString("以下是当前项目语义检索到的片段，只能作为定位线索；")
	builder.WriteString("引用结论前请用 read_files 读原文确认，并标注 file:line。\n")
	builder.WriteString("project_id: ")
	builder.WriteString(projectID)
	for index, doc := range docs {
		if used := len([]rune(builder.String())); used >= projectRetrieverMaxRunes {
			break
		}
		sourcePath := gconv.String(doc.MetaData["source_path"])
		chunkIndex := gconv.Int(doc.MetaData["chunk_index"])
		builder.WriteString(fmt.Sprintf("\n\n[%d] source=%s chunk=%d\n%s", index+1, sourcePath, chunkIndex, doc.Content))
	}
	return builder.String()
}
