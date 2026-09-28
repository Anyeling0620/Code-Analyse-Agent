package rag

import (
	"context"
	"crypto/sha1"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/vector"
	"edu.agent.code/common"
	"edu.agent.code/config"
	"edu.agent.code/service/dto"
	"edu.agent.code/utils/logger"
	"edu.agent.code/utils/sensitive"
	"encoding/hex"
	"fmt"
	fileloader "github.com/cloudwego/eino-ext/components/document/loader/file"
	docxparser "github.com/cloudwego/eino-ext/components/document/parser/docx"
	htmlparser "github.com/cloudwego/eino-ext/components/document/parser/html"
	recursplitter "github.com/cloudwego/eino-ext/components/document/transformer/splitter/recursive"
	"github.com/cloudwego/eino/components/document"
	einoparser "github.com/cloudwego/eino/components/document/parser"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	"github.com/gogf/gf/v2/util/gconv"
	"github.com/samber/lo"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	defaultDocRoot         = "docs"
	defaultChunkSize       = 1200
	defaultChunkOverlap    = 200
	defaultMaxFileBytes    = 10 * 1024 * 1024
	defaultTopK            = 5
	defaultMaxContentRunes = 6400
	// maxEmbeddingBatchSize 是单次 embedding 请求能提交的最大文档数。
	// 当前提供方（智谱 embedding-3）的 input 数组上限为 64 条，超过会直接返回 400，
	// 因此所有写入向量库的批量都必须按这个上限切分。
	maxEmbeddingBatchSize = 64
)

type Service struct {
	conf  config.RAG
	root  string
	store vector.IStore
}

var (
	docsParser   einoparser.Parser
	docsLoader   document.Loader
	docsSplitter document.Transformer
	initError    error
	docsOnce     sync.Once
)

func getOrCreateSplitter(chunkSize, chunkOverlap int) (document.Transformer, error) {
	sp, err := recursplitter.NewSplitter(context.Background(), &recursplitter.Config{
		ChunkSize:   chunkSize,
		OverlapSize: chunkOverlap,
	})
	if err != nil {
		return nil, fmt.Errorf("getOrCreateSplitter NewSplitter: %v", err)
	}
	return sp, nil
}

func initDocsPipeline(chunkSize, chunkOverlap int) (einoparser.Parser, document.Loader, document.Transformer, error) {
	docsOnce.Do(func() {
		htmlParser, err := htmlparser.NewParser(context.Background(), &htmlparser.Config{
			Selector: nil,
		})
		if err != nil {
			initError = fmt.Errorf("init html parser error: %v", err)
			return
		}
		docxParser, err := docxparser.NewDocxParser(context.Background(), &docxparser.Config{})
		if err != nil {
			initError = fmt.Errorf("init docx parser error: %v", err)
			return
		}
		extParser, err := einoparser.NewExtParser(context.Background(), &einoparser.ExtParserConfig{
			Parsers: map[string]einoparser.Parser{
				".html": htmlParser,
				".htm":  htmlParser,
				".docx": docxParser,
			},
			FallbackParser: einoparser.TextParser{},
		})
		if err != nil {
			initError = fmt.Errorf("init ext parser error: %v", err)
			return
		}
		loader, err := fileloader.NewFileLoader(context.Background(), &fileloader.FileLoaderConfig{
			Parser: extParser,
		})
		if err != nil {
			initError = fmt.Errorf("init loader error: %v", err)
			return
		}
		splitter, err := getOrCreateSplitter(chunkSize, chunkOverlap)
		if err != nil {
			initError = fmt.Errorf("fail to create splitter : %v", err)
			return
		}
		docsSplitter = splitter
		docsParser = extParser
		docsLoader = loader
	})
	return docsParser, docsLoader, docsSplitter, initError
}

func withDefault(conf config.RAG) config.RAG {
	if conf.DocsRoot == "" {
		conf.DocsRoot = defaultDocRoot
	}
	if conf.ChunkSize <= 0 {
		conf.ChunkSize = defaultChunkSize
	}
	if conf.ChunkOverlap <= 0 {
		conf.ChunkOverlap = defaultChunkOverlap
	}
	if conf.MaxFileBytes <= 0 {
		conf.MaxFileBytes = defaultMaxFileBytes
	}
	if conf.TopK <= 0 {
		conf.TopK = defaultTopK
	}
	if conf.MaxContextRunes <= 0 {
		conf.MaxContextRunes = defaultMaxContentRunes
	}
	return conf
}

func resolveDocsRoot(root string) (string, error) {
	if root == "" {
		root = defaultDocRoot
	}
	if !filepath.IsAbs(root) {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		root = filepath.Join(wd, filepath.FromSlash(root))
	}
	return filepath.Abs(root)
}

func NewService(ctx context.Context, adaptor adaptor.IAdaptor) (*Service, error) {
	conf := adaptor.GetConfig().RAG
	if !conf.Enabled {
		return &Service{}, nil
	}
	conf = withDefault(conf)
	root, err := resolveDocsRoot(conf.DocsRoot)
	if err != nil {
		return nil, err
	}
	store, err := vector.NewMilvus(ctx, adaptor, vector.WithInitCollection(true), vector.WithReranker(vector.NewReranker(conf.Rerank)))
	if err != nil {
		return nil, err
	}
	svc := &Service{
		conf:  conf,
		root:  root,
		store: store,
	}
	if conf.AutoIndexOnStartup {
		if err := svc.IndexDocs(ctx); err != nil {
			_ = store.Close()
			return nil, err
		}
		logger.Info("Auto indexing on startup completed")
	}
	return svc, nil
}

func (s *Service) Close() error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Close()
}

func (s *Service) IndexDocs(ctx context.Context) error {
	docs, err := loadDocs(ctx, s.root, s.conf)
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		logger.Warn("rag no docs chunks to index root %s", s.root)
		return nil
	}
	chunks := lo.Chunk(docs, maxEmbeddingBatchSize)
	ids := make([]string, 0, len(docs))
	for _, chunk := range chunks {
		tids, err := s.store.Store(ctx, chunk)
		if err != nil {
			return fmt.Errorf("failed to store chunk: %w", err)
		}
		ids = append(ids, tids...)
	}
	// TODO ids是否需要返回 请求处理 数据库还要处理
	return nil
}

func loadDocs(ctx context.Context, root string, conf config.RAG) ([]*schema.Document, error) {
	return loadDocsInScope(ctx, root, conf, docScope{})
}

// docScope 描述一次索引的来源范围。
// ProjectID 为空表示沿用全局 docs_root 的旧行为（不写项目元数据）。
type docScope struct {
	ProjectID   string
	ProjectRoot string
	Commit      string
}

// loadDocsInScope 取样并切分 root 下的文件；scope 非空时给每个 chunk 补项目元数据。
// 项目与全局索引共用同一条管线，区别只在元数据和 chunk ID 是否带项目标识。
func loadDocsInScope(ctx context.Context, root string, conf config.RAG, scope docScope) ([]*schema.Document, error) {
	// 先把文件弄出来
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", absRoot)
	}
	// 解析 分片 错误 加载器
	// TODO 解析器没用上
	_, loader, splitter, err := initDocsPipeline(conf.ChunkSize, conf.ChunkOverlap)
	if err != nil {
		return nil, fmt.Errorf("loadDocs initDocsPipeline error: %v", err)
	}
	docs := make([]*schema.Document, 0)
	err = filepath.WalkDir(absRoot, func(path string, info fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:

		}
		if path == absRoot {
			return nil
		}
		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			if shouldSkipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isDocFile(rel) || isSensitiveFile(rel) {
			return nil
		}
		fi, err := info.Info()
		if err != nil || fi.Size() > int64(conf.MaxFileBytes) {
			return nil
		}
		loaded, err := loader.Load(ctx, document.Source{URI: path})
		if err != nil {
			logger.Error("fail to load docs from %s", rel)
			return nil
		}
		if len(loaded) == 0 {
			return nil
		}
		n := 0
		for _, doc := range loaded {
			// TODO 脱敏
			doc.Content = strings.TrimSpace(sensitive.RedActText(doc.Content))
			if doc.Content != "" {
				loaded[n] = doc
				n++
			}
		}
		loaded = loaded[:n]
		if len(loaded) == 0 {
			return nil
		}
		fileChunks, err := buildFileChunks(ctx, splitter, rel, loaded, conf.ChunkSize, conf.ChunkOverlap)
		if err != nil {
			logger.Error("fail to split docs from %s", rel)
			return nil
		}
		for idx, chunk := range fileChunks {
			if chunk.Meta == nil {
				chunk.Meta = make(map[string]any)
			}
			chunk.Meta["header"] = chunkHeader(rel, chunk)
			chunk.Meta["source_path"] = rel
			chunk.Meta["chunk_index"] = idx
			chunk.Meta["chunk_size"] = len(chunk.Content)
			chunk.Meta["file_size"] = fi.Size()
			chunk.Meta["kind"] = chunk.Kind
			if chunk.Symbol != "" {
				chunk.Meta["symbol"] = chunk.Symbol
			}
			if chunk.Parent != "" {
				chunk.Meta["parent"] = chunk.Parent
			}
			if chunk.LineStart > 0 {
				chunk.Meta["line_start"] = chunk.LineStart
				chunk.Meta["line_end"] = chunk.LineEnd
			}
			if scope.ProjectID != "" {
				chunk.Meta[ProjectIDMetadataKey] = scope.ProjectID
				chunk.Meta[ProjectRootMetadataKey] = scope.ProjectRoot
				chunk.Meta[CommitMetadataKey] = scope.Commit
			}
			// chunk ID 必须带项目标识：Milvus 写入是 upsert（主键=文档 ID），
			// 否则两个项目里路径和内容相同的文件会互相覆盖。
			docs = append(docs, &schema.Document{
				ID:       chunkID(scope.ProjectID, path, idx, chunk.Content),
				Content:  chunk.Content,
				MetaData: chunk.Meta,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(docs, func(i, j int) bool {
		return docs[i].ID < docs[j].ID
	})
	return docs, nil
}

// fileChunk 是文件级切分结果：符号级切分会带上 symbol/kind/行号，回退路径只带内容。
type fileChunk struct {
	Content   string
	Symbol    string
	Kind      string
	Parent    string
	LineStart int
	LineEnd   int
	Meta      map[string]any
}

// buildFileChunks 优先按源码结构（符号级）切分；
// 语言不支持或解析不出结构时，回退到通用递归切分器，保持原有行为不变。
func buildFileChunks(ctx context.Context, splitter document.Transformer, rel string, loaded []*schema.Document, chunkSize, chunkOverlap int) ([]fileChunk, error) {
	if text := joinDocContents(loaded); strings.TrimSpace(text) != "" {
		if chunks, ok := splitBySymbol(rel, text, chunkSize, chunkOverlap); ok {
			out := make([]fileChunk, 0, len(chunks))
			for _, c := range chunks {
				out = append(out, fileChunk{
					Content:   c.Content,
					Symbol:    c.Symbol,
					Kind:      c.Kind,
					Parent:    c.Parent,
					LineStart: c.LineStart,
					LineEnd:   c.LineEnd,
				})
			}
			return out, nil
		}
	}

	chunks, err := splitter.Transform(ctx, loaded)
	if err != nil {
		return nil, err
	}
	out := make([]fileChunk, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, fileChunk{Content: c.Content, Kind: kindText, Meta: c.MetaData})
	}
	return out, nil
}

func joinDocContents(loaded []*schema.Document) string {
	switch len(loaded) {
	case 0:
		return ""
	case 1:
		return loaded[0].Content
	}
	var sb strings.Builder
	for _, doc := range loaded {
		sb.WriteString(doc.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

// chunkHeader 让 header 真正描述块内容：符号块用 "kind symbol"，
// 文件摘要块用文件名，回退块沿用原来的 Markdown 标题探测。
func chunkHeader(rel string, chunk fileChunk) string {
	switch {
	case chunk.Kind == kindFileSummary:
		return "file summary: " + rel
	case chunk.Symbol != "":
		return strings.TrimSpace(chunk.Kind + " " + chunk.Symbol)
	default:
		return detectHeading(chunk.Content)
	}
}

func chunkID(projectID, path string, idx int, content string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s:%s:%d:%s", projectID, path, idx, content)))
	return hex.EncodeToString(sum[:])
}

// TODO 如果有多级标题可能不符合预期
func detectHeading(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line := strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			return strings.TrimLeft(line, "#")
		}
	}
	return ""
}

// indexableExtensions 是可索引的扩展名集合：原有文档类型 + 代码/配置类型。
// 代码仓库里真正有信息量的是这些文件，只保留文档类型会让项目索引直接为空。
var indexableExtensions = map[string]bool{
	// 原有文档类型
	".html": true, ".htm": true, ".docx": true, ".txt": true, ".md": true, ".doc": true,
	// 代码
	".go": true, ".java": true, ".kt": true, ".py": true, ".js": true, ".jsx": true,
	".ts": true, ".tsx": true, ".vue": true, ".rs": true, ".c": true, ".h": true,
	".cpp": true, ".hpp": true, ".cs": true, ".rb": true, ".php": true, ".swift": true,
	".scala": true, ".sql": true, ".proto": true,
	// 配置与构建
	".yaml": true, ".yml": true, ".json": true, ".toml": true, ".ini": true,
	".sh": true, ".ps1": true, ".gradle": true, ".xml": true,
}

// indexableFileNames 是没有扩展名（或扩展名不具代表性）但必须入库的依赖/构建文件。
var indexableFileNames = map[string]bool{
	"go.mod": true, "package.json": true, "pom.xml": true, "requirements.txt": true,
	"cargo.toml": true, "build.gradle": true, "settings.gradle": true,
	"dockerfile": true, "docker-compose.yml": true, "docker-compose.yaml": true,
	"makefile": true, "procfile": true, ".env.example": true,
	// 无扩展名的仓库标配文件：filepath.Ext 返回空，若不在这里显式登记就会被整仓跳过。
	// 典型例子是只含一个 README 的仓库，跳过它等于索引到一个空库。
	"readme": true, "changelog": true,
	"notice": true, "authors": true, "contributors": true, "contributing": true,
	"codeowners": true, "gitignore": true, "vagrantfile": true, "gemfile": true,
	"rakefile": true, "jenkinsfile": true,
}

// sensitiveFileSuffixes / sensitiveFileNames 覆盖密钥类文件；.env 一律不入库，
// 但 .env.example 这类模板允许（它本身就是配置说明）。
var sensitiveFileSuffixes = []string{".pem", ".key", ".p12", ".pfx", ".keystore", ".jks"}

var sensitiveFileNames = map[string]bool{
	".env": true, "id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true,
}

func isDocFile(path string) bool {
	if indexableExtensions[strings.ToLower(filepath.Ext(path))] {
		return true
	}
	return indexableFileNames[strings.ToLower(filepath.Base(path))]
}

// isSensitiveFile 拦截密钥类文件：.env 只放行 .env.example 这样的模板。
func isSensitiveFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if sensitiveFileNames[base] {
		return true
	}
	if strings.HasPrefix(base, ".env.") && !strings.HasSuffix(base, ".example") {
		return true
	}
	for _, suffix := range sensitiveFileSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

func shouldSkipDir(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ".git", ".idea", ".vscode", ".obsidian", "node_modules", "vendor", "data", "dist", "build", "target":
		return true
	default:
		return false
	}
}

func (s *Service) Retriever(ctx context.Context, req *dto.RetrieverReq) ([]*dto.RetrieverChunkDto, common.Errno) {
	if s.store == nil {
		return nil, common.OK
	}
	chunks, err := s.store.Retrieve(ctx, req.Query, retriever.WithTopK(req.TopK))
	if err != nil {
		logger.Error("failed to retrieve chunks: %v", err)
		return nil, common.ServerError.WithError(err)
	}
	retChunks := make([]*dto.RetrieverChunkDto, 0, len(chunks))
	lo.ForEach(chunks, func(chunk *schema.Document, _ int) {
		retChunks = append(retChunks, &dto.RetrieverChunkDto{
			ID:         chunk.ID,
			Score:      chunk.Score(),
			Content:    chunk.Content,
			Header:     gconv.String(chunk.MetaData["header"]),
			ChunkIndex: gconv.Int(chunk.MetaData["chunk_index"]),
			ChunkSize:  gconv.Int(chunk.MetaData["chunk_size"]),
			FileSize:   gconv.Int(chunk.MetaData["file_size"]),
		})
	})

	return retChunks, common.OK
}
