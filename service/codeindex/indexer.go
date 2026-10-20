package codeindex

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"edu.agent.code/adaptor"
	sessionrepo "edu.agent.code/adaptor/repo/session"
	"edu.agent.code/common"
	"edu.agent.code/config"
	"edu.agent.code/service/do"
	"edu.agent.code/service/projectid"
	"edu.agent.code/utils/logger"

	"github.com/cloudwego/eino/components/tool"
)

const (
	defaultDBPath       = "data/code_index.db"
	defaultMaxFileBytes = int64(2 << 20)
	defaultMaxFiles     = 20000
	defaultMaxSymbols   = 200000
	defaultMaxResults   = 50
	// binarySniffBytes 是判断二进制文件的探测窗口：前 512 字节里有 NUL 基本就不是源码。
	binarySniffBytes = 512
)

// builtinSkipDirs 是无论如何都跳过的目录：版本库元数据、依赖目录、构建产物。
// 用户配置的 SkipDirs 追加在它后面，而不是替换——否则很容易因为少写一个 .git
// 就把整个仓库对象库当成源码索引一遍。
var builtinSkipDirs = []string{
	".git", ".svn", ".hg", "node_modules", "vendor", "dist", "build", "target",
	".venv", "venv", "__pycache__", ".idea", ".vscode", ".next", ".nuxt",
	"coverage", "bin", "obj", "out", ".gradle", ".mvn",
}

// IndexStatus 是索引状态机的取值。与 rag.IndexStatus 刻意分开：
// 两者互不影响，状态也不能混用。
type IndexStatus string

const (
	StatusNone     IndexStatus = "none"
	StatusIndexing IndexStatus = "indexing"
	StatusReady    IndexStatus = "ready"
	StatusFailed   IndexStatus = "failed"
)

// Status 是某个项目结构化索引的状态快照。
type Status struct {
	ProjectID   string
	Status      IndexStatus
	Commit      string
	FileCount   int
	SymbolCount int
	EdgeCount   int
	Truncated   bool
	IndexedAt   time.Time
	LastError   string
}

// sessionLookup 只依赖会话仓储里"当前项目"这一条读取能力，便于测试替换。
type sessionLookup interface {
	GetByID(ctx context.Context, sessionID string) (*do.SessionContext, error)
}

// Indexer 维护项目级结构化索引（符号表 + 依赖图），独立于 RAG。
//
// 内存里只缓存"这次进程内的状态"，权威数据在 SQLite：进程重启后 Status 会回查库，
// 因此重启不会把已有索引误判成未建立。
type Indexer struct {
	conf     config.CodeIndex
	base     adaptor.IAdaptor
	store    *store
	sessions sessionLookup

	mu sync.Mutex
	// running 保证同一项目同一时刻只有一个索引任务。
	running map[string]bool
	// statuses 是进程内的状态快照（indexing 只可能出现在这里）。
	statuses map[string]Status
	// sessionProject / rootProject 记录"哪个会话/目录对应哪个项目"，
	// 与 rag 的做法一致：索引时记住，查询时先用它，再回落到会话表。
	sessionProject map[string]Ref
	rootProject    map[string]string
}

// New 构造结构化索引器；conf.Enable=false 时返回 (nil, nil)，调用方据此不挂工具。
func New(conf config.CodeIndex, a adaptor.IAdaptor) (*Indexer, error) {
	if !conf.Enable {
		return nil, nil
	}
	if strings.TrimSpace(conf.Path) == "" {
		conf.Path = defaultDBPath
	}
	if conf.MaxFileBytes <= 0 {
		conf.MaxFileBytes = defaultMaxFileBytes
	}
	if conf.MaxFiles <= 0 {
		conf.MaxFiles = defaultMaxFiles
	}
	if conf.MaxSymbols <= 0 {
		conf.MaxSymbols = defaultMaxSymbols
	}
	if conf.MaxResults <= 0 {
		conf.MaxResults = defaultMaxResults
	}
	conf.SkipDirs = append(append([]string{}, builtinSkipDirs...), conf.SkipDirs...)

	st, err := openStore(conf.Path)
	if err != nil {
		return nil, err
	}
	indexer := &Indexer{
		conf:           conf,
		base:           a,
		store:          st,
		running:        make(map[string]bool),
		statuses:       make(map[string]Status),
		sessionProject: make(map[string]Ref),
		rootProject:    make(map[string]string),
	}
	if a != nil && a.GetDB() != nil {
		indexer.sessions = sessionrepo.NewSession(a)
	}
	return indexer, nil
}

// Close 关闭独立索引库连接。索引器为 nil（未启用）时是空操作。
func (ix *Indexer) Close() error {
	if ix == nil {
		return nil
	}
	return ix.store.close()
}

// EnsureIndexed 同步地为某个项目重建结构化索引。
//
//  1. 同一项目并发调用只跑一个，后来者直接返回；
//  2. 先扫描并抽取全部文件，再在一个事务里"删旧行 + 写新行"；
//  3. 失败只更新 last_error，旧索引保持原样可用。
//
// commit 只作记录，不参与"是否需要重建"的判断：一个项目只保留一份工作副本索引。
func (ix *Indexer) EnsureIndexed(ctx context.Context, ref Ref) error {
	if ix == nil {
		return nil
	}
	ref = ix.normalizeRef(ref)
	if ref.ProjectID == "" || ref.Root == "" {
		return fmt.Errorf("code index: project_id 与 root 不能为空")
	}
	ix.remember(ctx, ref)
	if !ix.acquire(ref.ProjectID) {
		logger.Info("code index: 该项目正在建索引，跳过本次请求 project_id=%s", ref.ProjectID)
		return nil
	}
	defer ix.release(ref.ProjectID)

	ix.setStatus(Status{ProjectID: ref.ProjectID, Status: StatusIndexing, Commit: ref.Commit})
	startedAt := time.Now()
	result, err := ix.build(ctx, ref)
	if err != nil {
		ix.setStatus(Status{
			ProjectID: ref.ProjectID,
			Status:    StatusFailed,
			Commit:    ref.Commit,
			LastError: err.Error(),
		})
		// 失败写入不能用已被取消的 ctx，否则最需要排障信息的时候反而写不进去。
		ix.store.markFailed(context.WithoutCancel(ctx), projectRow{
			ProjectID: ref.ProjectID,
			Root:      ref.Root,
			CommitSHA: ref.Commit,
		}, err.Error())
		return err
	}
	project := projectRow{
		ProjectID:   ref.ProjectID,
		Root:        ref.Root,
		CommitSHA:   ref.Commit,
		IndexedAt:   time.Now().Unix(),
		Truncated:   result.truncated,
		FileCount:   len(result.files),
		SymbolCount: len(result.symbols),
		EdgeCount:   len(result.edges),
	}
	if err := ix.store.replaceProject(ctx, project, result.files, result.symbols, result.edges); err != nil {
		ix.setStatus(Status{
			ProjectID: ref.ProjectID,
			Status:    StatusFailed,
			Commit:    ref.Commit,
			LastError: err.Error(),
		})
		ix.store.markFailed(context.WithoutCancel(ctx), project, err.Error())
		return err
	}
	ix.setStatus(Status{
		ProjectID:   ref.ProjectID,
		Status:      StatusReady,
		Commit:      ref.Commit,
		FileCount:   len(result.files),
		SymbolCount: len(result.symbols),
		EdgeCount:   len(result.edges),
		Truncated:   result.truncated,
		IndexedAt:   time.Now(),
	})
	logger.Info("code index: 索引完成 project_id=%s files=%d symbols=%d edges=%d truncated=%v elapsed=%s",
		ref.ProjectID, len(result.files), len(result.symbols), len(result.edges),
		result.truncated, time.Since(startedAt).Round(time.Millisecond))
	return nil
}

// EnsureIndexedAsync 在后台重建索引。调用方 ctx 通常来自 HTTP 请求，
// 这里再脱离一次取消信号：请求结束不该打断索引。
func (ix *Indexer) EnsureIndexedAsync(ctx context.Context, ref Ref) {
	if ix == nil {
		return
	}
	bg := context.WithoutCancel(ctx)
	go func() {
		if err := ix.EnsureIndexed(bg, ref); err != nil {
			logger.Error("code index: 后台建索引失败 project_id=%s root=%s err=%v",
				ref.ProjectID, ref.Root, err)
		}
	}()
}

// Status 返回项目索引状态：优先用进程内快照，其次回查 SQLite（重启后仍然有效）。
func (ix *Indexer) Status(ctx context.Context, projectID string) Status {
	if ix == nil {
		return Status{Status: StatusNone}
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return Status{Status: StatusNone}
	}
	ix.mu.Lock()
	status, ok := ix.statuses[projectID]
	ix.mu.Unlock()
	if ok {
		return status
	}
	row, found, err := ix.store.project(ctx, projectID)
	if err != nil || !found {
		return Status{ProjectID: projectID, Status: StatusNone}
	}
	status = Status{
		ProjectID:   projectID,
		Commit:      row.CommitSHA,
		FileCount:   row.FileCount,
		SymbolCount: row.SymbolCount,
		EdgeCount:   row.EdgeCount,
		Truncated:   row.Truncated,
		IndexedAt:   indexedAtTime(row.IndexedAt),
		LastError:   row.LastError,
	}
	if row.IndexedAt > 0 {
		status.Status = StatusReady
	} else if row.LastError != "" {
		status.Status = StatusFailed
	} else {
		status.Status = StatusNone
	}
	return status
}

// Tools 返回 4 个结构化索引查询工具；索引未启用时返回 nil。
func (ix *Indexer) Tools() []tool.BaseTool {
	if ix == nil {
		return nil
	}
	return ix.buildTools()
}

type buildResult struct {
	files     []fileRow
	symbols   []symbolRow
	edges     []edgeRow
	truncated bool
}

// build 扫描目录 → 抽取事实 → 解析 import → 组装待写入的行。不碰数据库。
func (ix *Indexer) build(ctx context.Context, ref Ref) (buildResult, error) {
	root, err := filepath.Abs(ref.Root)
	if err != nil {
		return buildResult{}, fmt.Errorf("code index: 解析项目根目录 %s: %w", ref.Root, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return buildResult{}, fmt.Errorf("code index: 读取项目根目录 %s: %w", root, err)
	}
	if !info.IsDir() {
		return buildResult{}, fmt.Errorf("code index: %s 不是目录", root)
	}

	skip := make(map[string]bool, len(ix.conf.SkipDirs))
	for _, dir := range ix.conf.SkipDirs {
		if name := strings.ToLower(strings.TrimSpace(dir)); name != "" {
			skip[name] = true
		}
	}

	result := buildResult{}
	oversized := 0
	totalSymbols := 0

	walkErr := filepath.WalkDir(root, func(full string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// 单个文件/目录读不了不该让整次索引失败：跳过并继续。
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if entry.IsDir() {
			if full == root {
				return nil
			}
			if skip[strings.ToLower(entry.Name())] {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		lang, known := LanguageFor(rel)
		if !known {
			return nil
		}
		if !ix.extAllowed(rel) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if info.Size() > ix.conf.MaxFileBytes {
			oversized++
			result.truncated = true
			return nil
		}
		data, err := os.ReadFile(full)
		if err != nil {
			logger.Debug("code index: 读取文件失败，跳过 path=%s err=%v", rel, err)
			return nil
		}
		if looksBinary(data) {
			return nil
		}
		if len(result.files) >= ix.conf.MaxFiles {
			result.truncated = true
			logger.Warn("code index: 文件数达到上限 %d，停止扫描 root=%s", ix.conf.MaxFiles, root)
			return filepath.SkipAll
		}
		// 抽取失败（语言有映射但解析器不支持）仍然登记文件：
		// 它在依赖图里仍然是合法的 import 目标，只是没有符号。
		facts, _ := Extract(rel, data)
		lineCount := bytes.Count(data, []byte("\n"))
		if len(data) > 0 && data[len(data)-1] != '\n' {
			lineCount++
		}
		result.files = append(result.files, fileRow{
			ProjectID:   ref.ProjectID,
			Path:        rel,
			Language:    firstNonEmpty(facts.Language, lang),
			Size:        info.Size(),
			LineCount:   lineCount,
			SymbolCount: len(facts.Symbols),
		})
		for _, symbol := range facts.Symbols {
			if strings.TrimSpace(symbol.Name) == "" {
				continue
			}
			result.symbols = append(result.symbols, symbolRow{
				ProjectID: ref.ProjectID,
				Path:      rel,
				Name:      symbol.Name,
				Kind:      symbol.Kind,
				Parent:    symbol.Parent,
				Signature: symbol.Signature,
				LineStart: symbol.LineStart,
				LineEnd:   symbol.LineEnd,
			})
		}
		totalSymbols += len(facts.Symbols)
		for _, edge := range facts.Edges {
			if strings.TrimSpace(edge.ToRaw) == "" {
				continue
			}
			result.edges = append(result.edges, edgeRow{
				ProjectID:  ref.ProjectID,
				FromPath:   rel,
				FromSymbol: edge.FromSymbol,
				ToRaw:      strings.TrimSpace(edge.ToRaw),
				Kind:       edge.Kind,
				Confidence: normalizeConfidence(edge.Kind, edge.Confidence),
				Line:       edge.Line,
			})
		}
		if totalSymbols >= ix.conf.MaxSymbols {
			result.truncated = true
			logger.Warn("code index: 符号数达到上限 %d，停止扫描 root=%s", ix.conf.MaxSymbols, root)
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return buildResult{}, fmt.Errorf("code index: 扫描 %s: %w", root, walkErr)
	}
	if oversized > 0 {
		logger.Info("code index: 有 %d 个文件超过单文件上限 %d 字节，已跳过 project_id=%s",
			oversized, ix.conf.MaxFileBytes, ref.ProjectID)
	}

	// 文件集合齐了之后再解析 import：Go 的 package 目录、TS 的 index 文件
	// 都需要"库里有那些文件"这个前提才能反推本地路径。
	index := newPathIndex(result.files, root)
	for i := range result.edges {
		edge := &result.edges[i]
		if edge.Kind != EdgeImport {
			continue
		}
		edge.ToPath = index.resolve(edge.FromPath, edge.ToRaw)
	}
	return result, nil
}

// extAllowed 在 LanguageFor 之后再做一次配置层面的扩展名过滤。
func (ix *Indexer) extAllowed(rel string) bool {
	if len(ix.conf.IncludeExts) == 0 {
		return true
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(rel), "."))
	for _, want := range ix.conf.IncludeExts {
		want = strings.ToLower(strings.TrimSpace(want))
		want = strings.TrimPrefix(want, ".")
		if want != "" && want == ext {
			return true
		}
	}
	return false
}

// looksBinary 只嗅探前 512 字节：源码文件不会出现 NUL，出现即视为二进制。
func looksBinary(data []byte) bool {
	if len(data) > binarySniffBytes {
		data = data[:binarySniffBytes]
	}
	return bytes.IndexByte(data, 0) >= 0
}

func normalizeConfidence(kind, confidence string) string {
	confidence = strings.TrimSpace(confidence)
	if confidence != "" {
		return confidence
	}
	// import 是源码里明写的，按精确处理；类型关系很多靠正则推断，默认按启发式，
	// 宁可让模型知道"这是猜的"，也不要伪装成事实。
	if kind == EdgeImport {
		return ConfidenceExact
	}
	return ConfidenceHeuristic
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (ix *Indexer) acquire(projectID string) bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.running[projectID] {
		return false
	}
	ix.running[projectID] = true
	return true
}

func (ix *Indexer) release(projectID string) {
	ix.mu.Lock()
	delete(ix.running, projectID)
	ix.mu.Unlock()
}

func (ix *Indexer) setStatus(status Status) {
	ix.mu.Lock()
	ix.statuses[status.ProjectID] = status
	ix.mu.Unlock()
}

// remember 记录 session→项目 与 root→project_id，供后续查询解析使用。
func (ix *Indexer) remember(ctx context.Context, ref Ref) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ref.Root != "" {
		ix.rootProject[normalizeProjectRoot(ref.Root)] = ref.ProjectID
	}
	if sessionID := common.SessionIDFromContext(ctx); sessionID != "" {
		ix.sessionProject[sessionID] = ref
	}
}

// resolveCurrent 解析"当前会话在分析哪个项目"：
//  1. 本进程内建过索引的会话（第一轮就能命中，不依赖会话表是否已落库）；
//  2. 会话表里的当前项目根目录（跨进程、追问时命中）。
//
// 这条路径完全独立于 RAG，rag.enabled=false 时同样可用。
func (ix *Indexer) resolveCurrent(ctx context.Context) (Ref, bool) {
	if ix == nil {
		return Ref{}, false
	}
	sessionID := common.SessionIDFromContext(ctx)
	if sessionID == "" {
		return Ref{}, false
	}
	ix.mu.Lock()
	ref, ok := ix.sessionProject[sessionID]
	ix.mu.Unlock()
	if ok && ref.ProjectID != "" {
		return ref, true
	}
	if ix.sessions == nil {
		return Ref{}, false
	}
	session, err := ix.sessions.GetByID(ctx, sessionID)
	if err != nil || session == nil {
		return Ref{}, false
	}
	ref = ix.refFromRoot(session.CurrentProjectRoot)
	if ref.ProjectID == "" {
		return Ref{}, false
	}
	return ref, true
}

func (ix *Indexer) normalizeRef(ref Ref) Ref {
	ref.ProjectID = strings.TrimSpace(ref.ProjectID)
	ref.Commit = strings.TrimSpace(ref.Commit)
	ref.Root = strings.TrimSpace(ref.Root)
	if ref.Root != "" {
		if abs, err := filepath.Abs(ref.Root); err == nil {
			ref.Root = filepath.Clean(abs)
		}
	}
	if ref.ProjectID == "" {
		ref.ProjectID = ix.refFromRoot(ref.Root).ProjectID
	}
	return ref
}

// refFromRoot 从本地根目录反推项目标识：优先用本进程登记过的映射，
// 再退化为"git remote 优先、否则本地路径"的派生规则（与 repo_fetch / rag 一致）。
func (ix *Indexer) refFromRoot(root string) Ref {
	root = strings.TrimSpace(root)
	if root == "" {
		return Ref{}
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = filepath.Clean(abs)
	}
	ix.mu.Lock()
	projectID := ix.rootProject[normalizeProjectRoot(root)]
	ix.mu.Unlock()
	if projectID != "" {
		return Ref{ProjectID: projectID, Root: root}
	}
	return Ref{ProjectID: projectid.Derive(root, gitOriginRemote(root)), Root: root}
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

// pathIndex 是"项目内文件集合"的索引，用于把 import 原始文本解析成本地路径。
// 解析不出来时返回空串：调用方会如实展示"未解析"，而不是编一个路径。
type pathIndex struct {
	paths map[string]bool
	dirs  map[string]bool
	// noExt 是去掉扩展名的路径（小写），用于 Java/C# 这种按类名引用的语言。
	noExt map[string]string
	// suffix 是任意路径后缀（小写）→ 完整路径，用于 C 头文件、Go 包名这类
	// 只给了相对片段的引用。同名冲突时保留排序靠前的一个。
	suffix map[string]string
	module string
}

func newPathIndex(files []fileRow, root string) *pathIndex {
	index := &pathIndex{
		paths:  make(map[string]bool, len(files)),
		dirs:   make(map[string]bool, len(files)),
		noExt:  make(map[string]string, len(files)),
		suffix: make(map[string]string, len(files)*4),
		module: readModuleName(root),
	}
	for _, file := range files {
		index.paths[file.Path] = true
		for dir := path.Dir(file.Path); dir != "." && dir != "/" && dir != ""; dir = path.Dir(dir) {
			index.dirs[dir] = true
		}
		noExtKey := strings.ToLower(strings.TrimSuffix(file.Path, path.Ext(file.Path)))
		if _, exists := index.noExt[noExtKey]; !exists {
			index.noExt[noExtKey] = file.Path
		}
		parts := strings.Split(file.Path, "/")
		for i := 0; i < len(parts); i++ {
			key := strings.ToLower(strings.Join(parts[i:], "/"))
			if _, exists := index.suffix[key]; !exists {
				index.suffix[key] = file.Path
			}
		}
	}
	return index
}

// resolve 把一条 import 原文解析成本地路径；目录以 "/" 结尾。
func (i *pathIndex) resolve(fromPath, raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.Trim(raw, "'\"<>`")
	raw = strings.TrimSuffix(raw, ";")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	fromDir := path.Dir(fromPath)
	switch strings.ToLower(path.Ext(fromPath)) {
	case ".go":
		return i.resolveGo(fromDir, raw)
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".vue", ".svelte":
		return i.resolveRelative(fromDir, raw)
	case ".py":
		return i.resolvePython(fromDir, raw)
	case ".java", ".kt", ".kts", ".scala", ".cs", ".swift":
		return i.resolveDotted(raw)
	case ".rs":
		return i.resolveRust(fromDir, raw)
	default:
		return i.resolveGeneric(fromDir, raw)
	}
}

func (i *pathIndex) resolveGo(fromDir, raw string) string {
	if strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") {
		return i.resolveRelative(fromDir, raw)
	}
	if i.module != "" {
		if raw == i.module {
			return ""
		}
		if strings.HasPrefix(raw, i.module+"/") {
			rel := strings.TrimPrefix(raw, i.module+"/")
			if i.dirs[rel] {
				return rel + "/"
			}
			return i.lookupSuffix(rel)
		}
	}
	// 标准库/第三方包不在项目里，按路径后缀兜底（处理 vendor 之外的本地布局）。
	return i.lookupSuffix(raw)
}

// resolveRelative 处理 "./x" "../x" 这类相对引用，覆盖 TS/JS/C/通用写法。
func (i *pathIndex) resolveRelative(fromDir, raw string) string {
	if !strings.HasPrefix(raw, ".") && !strings.HasPrefix(raw, "/") {
		return i.resolveGeneric(fromDir, raw)
	}
	base := path.Clean(path.Join(fromDir, raw))
	if i.paths[base] {
		return base
	}
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".vue", ".svelte", ".json", ".css", ".scss"} {
		if i.paths[base+ext] {
			return base + ext
		}
	}
	// 目录引用优先落到 index.*：TS/JS 的 "./lib" 通常就是 lib/index.ts，
	// 精确到文件比返回整个目录更有用（目录形式仍保留给 Go 那种 package 导入）。
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"} {
		candidate := path.Join(base, "index"+ext)
		if i.paths[candidate] {
			return candidate
		}
	}
	if i.dirs[base] {
		return base + "/"
	}
	return ""
}

func (i *pathIndex) resolvePython(fromDir, raw string) string {
	dots := 0
	for dots < len(raw) && raw[dots] == '.' {
		dots++
	}
	if dots > 0 {
		base := fromDir
		for step := 1; step < dots; step++ {
			base = path.Dir(base)
		}
		rest := strings.ReplaceAll(raw[dots:], ".", "/")
		return i.pythonCandidates(path.Clean(path.Join(base, rest)))
	}
	rel := strings.ReplaceAll(raw, ".", "/")
	if target := i.pythonCandidates(rel); target != "" {
		return target
	}
	// 顶层包之外的项目常有 app/ 之类的前缀目录，按后缀再找一次。
	if target := i.lookupSuffix(rel + ".py"); target != "" {
		return target
	}
	return i.lookupSuffix(path.Join(rel, "__init__.py"))
}

func (i *pathIndex) pythonCandidates(rel string) string {
	if i.paths[rel+".py"] {
		return rel + ".py"
	}
	if init := path.Join(rel, "__init__.py"); i.paths[init] {
		return init
	}
	if i.dirs[rel] {
		return rel + "/"
	}
	return ""
}

// resolveDotted 处理 Java/Kotlin/C# 这种 com.foo.Bar 的点分引用。
// 只能精确到"类名对应的文件"，方法/内部类引用会退化为无法解析。
func (i *pathIndex) resolveDotted(raw string) string {
	if strings.HasPrefix(raw, ".") {
		return ""
	}
	rel := strings.ReplaceAll(raw, ".", "/")
	if target := i.noExt[strings.ToLower(rel)]; target != "" {
		return target
	}
	if i.dirs[rel] {
		return rel + "/"
	}
	return ""
}

func (i *pathIndex) resolveRust(fromDir, raw string) string {
	raw = strings.TrimPrefix(raw, "::")
	raw = strings.TrimPrefix(raw, "crate::")
	raw = strings.TrimPrefix(raw, "self::")
	for strings.HasPrefix(raw, "super::") {
		raw = strings.TrimPrefix(raw, "super::")
		fromDir = path.Dir(fromDir)
	}
	rel := path.Clean(path.Join(fromDir, strings.ReplaceAll(raw, "::", "/")))
	if i.paths[rel+".rs"] {
		return rel + ".rs"
	}
	mod := path.Join(rel, "mod.rs")
	if i.paths[mod] {
		return mod
	}
	if i.dirs[rel] {
		return rel + "/"
	}
	return ""
}

// resolveGeneric 处理 C/C++ 头文件、以及"看起来像相对路径"的 import。
func (i *pathIndex) resolveGeneric(fromDir, raw string) string {
	if i.paths[raw] {
		return raw
	}
	joined := path.Clean(path.Join(fromDir, raw))
	if i.paths[joined] {
		return joined
	}
	if i.dirs[joined] {
		return joined + "/"
	}
	return i.lookupSuffix(raw)
}

// lookupSuffix 用后缀表匹配：同一个后缀命中多个文件时返回第一个。
func (i *pathIndex) lookupSuffix(raw string) string {
	key := strings.ToLower(strings.TrimPrefix(raw, "/"))
	if target := i.suffix[key]; target != "" {
		return target
	}
	if i.dirs[raw] {
		return raw + "/"
	}
	return ""
}

// readModuleName 读取 go.mod 的 module 声明；没有 go.mod 时返回空。
func readModuleName(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// gitOriginRemote 从 .git/config 读取 origin 地址，只读文件、不执行命令：
// repo_fetch 用 remote 派生项目 ID，查询侧必须拿到同一个 ID 才能命中同一份索引。
// worktree/submodule 里 .git 是文件、读不到内容，会自然退化为按本地路径派生。
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

func isOriginSection(line string) bool {
	line = strings.TrimSpace(line)
	if len(line) < 2 || !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
		return false
	}
	inner := strings.TrimSpace(line[1 : len(line)-1])
	if len(inner) < len("remote") || !strings.EqualFold(inner[:len("remote")], "remote") {
		return false
	}
	rest := strings.Trim(strings.TrimSpace(inner[len("remote"):]), "'\"")
	return strings.EqualFold(rest, "origin")
}

// sortSymbols 让输出稳定：先按行号，再按名称（同一行可能有多个声明）。
func sortSymbols(rows []symbolRow) {
	sort.SliceStable(rows, func(a, b int) bool {
		if rows[a].LineStart != rows[b].LineStart {
			return rows[a].LineStart < rows[b].LineStart
		}
		return rows[a].Name < rows[b].Name
	})
}
