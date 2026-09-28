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
	chunks := lo.Chunk(docs, 100)
	ids := make([]string, 0, len(docs))
	for _, chunk := range chunks {
		tids, err := s.store.Store(ctx, chunk)
		if err != nil {
			return fmt.Errorf("failed to store chunk")
		}
		ids = append(ids, tids...)
	}
	// TODO ids是否需要返回 请求处理 数据库还要处理
	return nil
}

func loadDocs(ctx context.Context, root string, conf config.RAG) ([]*schema.Document, error) {
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
		if !isDocFile(rel) {
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
		chunks, err := splitter.Transform(ctx, loaded)
		if err != nil {
			logger.Error("fail to split docs from %s", rel)
			return nil
		}
		for idx, chunk := range chunks {
			header := detectHeading(chunk.Content)
			if chunk.MetaData == nil {
				chunk.MetaData = make(map[string]any)
			}
			chunk.MetaData["header"] = header
			chunk.MetaData["source_path"] = rel
			chunk.MetaData["chunk_index"] = idx
			chunk.MetaData["chunk_size"] = len(chunk.Content)
			chunk.MetaData["file_size"] = fi.Size()
			chunk.ID = chunkID(path, idx, chunk.Content)
			docs = append(docs, chunk)
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

func chunkID(path string, idx int, content string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s:%d:%s", path, idx, content)))
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

func isDocFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".html", ".htm", ".docx", ".txt", ".md", ".doc":
		return true
	default:
		return false
	}
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
