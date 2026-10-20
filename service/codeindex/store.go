package codeindex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"edu.agent.code/utils/logger"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// 表名单独列出：这几张表只存在于独立的 code_index.db 里，不与业务库混用，
// 因此没有复用 adaptor/repo/model 下的模型。
const (
	tableProjects = "code_index_projects"
	tableFiles    = "code_index_files"
	tableSymbols  = "code_index_symbols"
	tableEdges    = "code_index_edges"
)

// projectRow 是项目索引的元信息：一个项目在库里只有一行。
// indexed_at 为 0 表示从未成功建过索引（失败时只写 last_error）。
type projectRow struct {
	ProjectID   string `gorm:"column:project_id;type:text;primaryKey"`
	Root        string `gorm:"column:root;type:text;not null"`
	CommitSHA   string `gorm:"column:commit_sha;type:text"`
	FileCount   int    `gorm:"column:file_count;not null;default:0"`
	SymbolCount int    `gorm:"column:symbol_count;not null;default:0"`
	EdgeCount   int    `gorm:"column:edge_count;not null;default:0"`
	IndexedAt   int64  `gorm:"column:indexed_at;not null;default:0"`
	// Truncated 表示本次索引触到了 max_files / max_symbols 上限，索引不完整；
	// 查询侧需要如实告知模型，避免把"只有一半符号"当成全量。
	Truncated bool   `gorm:"column:truncated;not null;default:false"`
	LastError string `gorm:"column:last_error;type:text"`
}

func (projectRow) TableName() string { return tableProjects }

// fileRow 是一个被索引的文件。唯一索引 (project_id, path) 保证重跑不会产生重复行。
type fileRow struct {
	ID          int64  `gorm:"column:id;primaryKey;autoIncrement"`
	ProjectID   string `gorm:"column:project_id;type:text;not null;uniqueIndex:idx_code_files_project_path,priority:1"`
	Path        string `gorm:"column:path;type:text;not null;uniqueIndex:idx_code_files_project_path,priority:2"`
	Language    string `gorm:"column:language;type:text"`
	Size        int64  `gorm:"column:size;not null;default:0"`
	LineCount   int    `gorm:"column:line_count;not null;default:0"`
	SymbolCount int    `gorm:"column:symbol_count;not null;default:0"`
}

func (fileRow) TableName() string { return tableFiles }

// symbolRow 是一个声明。Parent 非空表示这是类型/类/接口内部的方法或字段。
type symbolRow struct {
	ID        int64  `gorm:"column:id;primaryKey;autoIncrement"`
	ProjectID string `gorm:"column:project_id;type:text;not null;index:idx_code_symbols_project_name,priority:1;index:idx_code_symbols_project_path,priority:1"`
	Path      string `gorm:"column:path;type:text;not null;index:idx_code_symbols_project_path,priority:2"`
	Name      string `gorm:"column:name;type:text;not null;index:idx_code_symbols_project_name,priority:2"`
	Kind      string `gorm:"column:kind;type:text"`
	Parent    string `gorm:"column:parent;type:text"`
	Signature string `gorm:"column:signature;type:text"`
	LineStart int    `gorm:"column:line_start;not null;default:0"`
	LineEnd   int    `gorm:"column:line_end;not null;default:0"`
}

func (symbolRow) TableName() string { return tableSymbols }

// edgeRow 是一条有向关系。from_symbol 为空表示文件级依赖（import），
// to_path 是写入时按语言规则解析出的本地路径；解析不出来时为空。
//
// to_path 以 "/" 结尾表示它指向一个目录（例如 Go 的 package import），
// 查询入边时必须按目录前缀匹配，而不是等值匹配。
type edgeRow struct {
	ID         int64  `gorm:"column:id;primaryKey;autoIncrement"`
	ProjectID  string `gorm:"column:project_id;type:text;not null;index:idx_code_edges_project_from,priority:1;index:idx_code_edges_project_to,priority:1;index:idx_code_edges_project_symbol,priority:1"`
	FromPath   string `gorm:"column:from_path;type:text;not null;index:idx_code_edges_project_from,priority:2"`
	FromSymbol string `gorm:"column:from_symbol;type:text;index:idx_code_edges_project_symbol,priority:2"`
	ToRaw      string `gorm:"column:to_raw;type:text"`
	ToPath     string `gorm:"column:to_path;type:text;index:idx_code_edges_project_to,priority:2"`
	Kind       string `gorm:"column:kind;type:text"`
	Confidence string `gorm:"column:confidence;type:text"`
	Line       int    `gorm:"column:line;not null;default:0"`
}

func (edgeRow) TableName() string { return tableEdges }

// store 只封装这个独立 SQLite 文件的读写，不依赖业务库连接。
type store struct {
	db *gorm.DB
}

// openStore 打开（必要时创建）独立索引库并建表。
//
// DSN 里的 busy_timeout 应对并发写（索引事务 vs 查询），WAL 让查询不被写事务阻塞；
// 这两项都必须在打开时带上，事后 PRAGMA 对已存在的连接池不一定生效。
func openStore(path string) (*store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("code index: 数据库路径为空")
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("code index: 创建目录 %s: %w", dir, err)
		}
	}
	dsn := path
	if !strings.Contains(dsn, "?") {
		dsn += "?_busy_timeout=5000&_journal_mode=WAL"
	}
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("code index: 打开 sqlite %s: %w", path, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("code index: 取 sql 连接 %s: %w", path, err)
	}
	// WAL 下读写可以并行，但仍限制连接数，避免无节制增长。
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(2)
	if err := db.AutoMigrate(&projectRow{}, &fileRow{}, &symbolRow{}, &edgeRow{}); err != nil {
		return nil, fmt.Errorf("code index: 建表 %s: %w", path, err)
	}
	return &store{db: db}, nil
}

func (s *store) close() error {
	if s == nil || s.db == nil {
		return nil
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// replaceProject 在一个事务里完成「删旧行 + 写新行 + 更新项目统计」。
//
// 整体事务是必须的：索引失败时旧索引应当保持完整可用，而不是被删了一半。
func (s *store) replaceProject(ctx context.Context, project projectRow, files []fileRow, symbols []symbolRow, edges []edgeRow) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("code index: store 未初始化")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, table := range []string{tableFiles, tableSymbols, tableEdges} {
			if err := tx.Exec("DELETE FROM "+table+" WHERE project_id = ?", project.ProjectID).Error; err != nil {
				return fmt.Errorf("清理旧索引 %s: %w", table, err)
			}
		}
		if len(files) > 0 {
			if err := tx.CreateInBatches(files, 500).Error; err != nil {
				return fmt.Errorf("写入文件表: %w", err)
			}
		}
		if len(symbols) > 0 {
			if err := tx.CreateInBatches(symbols, 500).Error; err != nil {
				return fmt.Errorf("写入符号表: %w", err)
			}
		}
		if len(edges) > 0 {
			if err := tx.CreateInBatches(edges, 500).Error; err != nil {
				return fmt.Errorf("写入关系表: %w", err)
			}
		}
		project.FileCount = len(files)
		project.SymbolCount = len(symbols)
		project.EdgeCount = len(edges)
		if err := tx.Save(&project).Error; err != nil {
			return fmt.Errorf("更新项目索引元信息: %w", err)
		}
		return nil
	})
}

// markFailed 只更新失败原因，保留上一次成功索引的统计与时间：
// 索引构建失败不代表旧数据不可用，工具侧仍可降级查询。
func (s *store) markFailed(ctx context.Context, project projectRow, reason string) {
	if s == nil || s.db == nil {
		return
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing projectRow
		res := tx.Where("project_id = ?", project.ProjectID).Limit(1).Find(&existing)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			return tx.Model(&projectRow{}).Where("project_id = ?", project.ProjectID).
				Update("last_error", reason).Error
		}
		project.LastError = reason
		return tx.Create(&project).Error
	})
	if err != nil {
		logger.Warn("code index: 记录失败状态失败 project_id=%s err=%v", project.ProjectID, err)
	}
}

func (s *store) project(ctx context.Context, projectID string) (projectRow, bool, error) {
	if s == nil || s.db == nil {
		return projectRow{}, false, fmt.Errorf("code index: store 未初始化")
	}
	var row projectRow
	res := s.db.WithContext(ctx).Where("project_id = ?", projectID).Limit(1).Find(&row)
	if res.Error != nil {
		return projectRow{}, false, res.Error
	}
	return row, res.RowsAffected > 0, nil
}

// fileSymbols 取某个文件的符号，按行号排序（声明顺序即阅读顺序）。
func (s *store) fileSymbols(ctx context.Context, projectID, path string) ([]symbolRow, error) {
	var rows []symbolRow
	err := s.db.WithContext(ctx).Where("project_id = ? AND path = ?", projectID, path).
		Order("line_start, id").Find(&rows).Error
	return rows, err
}

// hasFile 判断某个路径是否真的被索引过；用于把"路径不存在"和"没有符号"区分开。
func (s *store) hasFile(ctx context.Context, projectID, path string) (bool, error) {
	var total int64
	err := s.db.WithContext(ctx).Model(&fileRow{}).
		Where("project_id = ? AND path = ?", projectID, path).Count(&total).Error
	return total > 0, err
}

// filesUnder 列出目录下的文件（含子目录），按路径排序。
// dir 为空表示项目根；limit<=0 表示不限。
func (s *store) filesUnder(ctx context.Context, projectID, dir string, limit int) ([]fileRow, error) {
	query := s.db.WithContext(ctx).Model(&fileRow{}).Where("project_id = ?", projectID)
	if dir != "" {
		prefix := strings.TrimSuffix(dir, "/") + "/"
		// 用 instr 做前缀判断，避免 path 里的 % / _ 被 LIKE 当成通配符。
		query = query.Where("instr(path, ?) = 1", prefix)
	}
	query = query.Order("path")
	if limit > 0 {
		query = query.Limit(limit)
	}
	var rows []fileRow
	err := query.Find(&rows).Error
	return rows, err
}

// countFilesUnder 用于告诉模型目录里被截断了多少文件。
func (s *store) countFilesUnder(ctx context.Context, projectID, dir string) (int64, error) {
	query := s.db.WithContext(ctx).Model(&fileRow{}).Where("project_id = ?", projectID)
	if dir != "" {
		query = query.Where("instr(path, ?) = 1", strings.TrimSuffix(dir, "/")+"/")
	}
	var total int64
	err := query.Count(&total).Error
	return total, err
}

// symbolsForPaths 一次取回多个文件的符号，避免目录 outline 时逐个文件查库。
func (s *store) symbolsForPaths(ctx context.Context, projectID string, paths []string) (map[string][]symbolRow, error) {
	result := make(map[string][]symbolRow, len(paths))
	if len(paths) == 0 {
		return result, nil
	}
	var rows []symbolRow
	err := s.db.WithContext(ctx).
		Where("project_id = ? AND path IN ?", projectID, paths).
		Order("path, line_start, id").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.Path] = append(result[row.Path], row)
	}
	return result, nil
}

// findSymbols 分三级匹配：等值 → 前缀 → 包含。
// 逐级查询，某一级已经凑够 limit 就不再往下，避免"包含"把结果冲成噪音。
func (s *store) findSymbols(ctx context.Context, projectID, name, kind string, limit int) ([]symbolRow, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	// 用 instr 而不是 LIKE：符号名里可能带 $ / _ 这类字符，转义规则容易写错，
	// instr 是字面量匹配，不需要额外转义。lower 让匹配对大小写不敏感（Java/TS 常见）。
	tiers := []struct {
		clause string
		args   []any
	}{
		{"lower(name) = lower(?)", []any{name}},
		{"instr(lower(name), lower(?)) = 1", []any{name}},
		{"instr(lower(name), lower(?)) > 0", []any{name}},
	}
	seen := make(map[string]bool)
	rows := make([]symbolRow, 0, limit)
	for _, tier := range tiers {
		if len(rows) >= limit {
			break
		}
		query := s.db.WithContext(ctx).Model(&symbolRow{}).
			Where("project_id = ?", projectID).
			Where(tier.clause, tier.args...)
		if kind != "" {
			query = query.Where("kind = ?", kind)
		}
		var batch []symbolRow
		if err := query.Order("length(name), name, path, line_start").
			Limit(limit - len(rows)).Find(&batch).Error; err != nil {
			return nil, err
		}
		for _, row := range batch {
			key := fmt.Sprintf("%s|%s|%d", row.Path, row.Name, row.LineStart)
			if seen[key] {
				continue
			}
			seen[key] = true
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// importsFrom 取一个文件的所有出边（import）。
func (s *store) importsFrom(ctx context.Context, projectID, path string) ([]edgeRow, error) {
	var rows []edgeRow
	err := s.db.WithContext(ctx).
		Where("project_id = ? AND from_path = ? AND kind = ?", projectID, path, EdgeImport).
		Order("line, id").Find(&rows).Error
	return rows, err
}

// importsInto 取"谁 import 了这个文件"。
//
// to_path 可能是目录（以 "/" 结尾）：Go 的 import 指向 package 而不是单个文件，
// 所以除了等值匹配，还要匹配目录前缀。
func (s *store) importsInto(ctx context.Context, projectID, path string) ([]edgeRow, error) {
	var rows []edgeRow
	err := s.db.WithContext(ctx).
		Where("project_id = ? AND kind = ?", projectID, EdgeImport).
		Where("to_path = ? OR (length(to_path) < length(?) AND substr(?, 1, length(to_path)) = to_path AND substr(to_path, -1) = '/')",
			path, path, path).
		Order("from_path, line, id").Find(&rows).Error
	return rows, err
}

// typeEdgesFrom 取某个类型出发的类型级关系（继承/实现/嵌入）。
func (s *store) typeEdgesFrom(ctx context.Context, projectID, name string) ([]edgeRow, error) {
	var rows []edgeRow
	// from_symbol 可能是 "Type.Method" 这种限定写法，按末段匹配。
	err := s.db.WithContext(ctx).
		Where("project_id = ? AND kind IN ?", projectID,
			[]string{EdgeExtends, EdgeImplements, EdgeEmbeds}).
		Where("from_symbol = ? OR substr(from_symbol, -?) = ?", name, len(name)+1, "."+name).
		Order("kind, to_raw").Find(&rows).Error
	return rows, err
}

// typeEdgesInto 取指向某个类型的类型级关系。
//
// to_raw 是源码里的原始写法（可能是 pkg.Base、crate::Base、Base[T]），无法完美还原，
// 因此这里先按原始名全库取回类型级边，再在内存里按名称末段匹配，属于启发式。
func (s *store) typeEdgesInto(ctx context.Context, projectID, name string) ([]edgeRow, error) {
	var rows []edgeRow
	err := s.db.WithContext(ctx).
		Where("project_id = ? AND kind IN ?", projectID,
			[]string{EdgeExtends, EdgeImplements, EdgeEmbeds}).
		Where("lower(to_raw) LIKE ? OR lower(to_raw) LIKE ? OR lower(to_raw) LIKE ? OR lower(to_raw) LIKE ?",
			strings.ToLower(name),
			"%."+strings.ToLower(name),
			"%::"+strings.ToLower(name),
			"%/"+strings.ToLower(name)).
		Order("from_path, line, id").Find(&rows).Error
	return rows, err
}

// indexedAtTime 把库里的 unix 秒转成时间，0 值保持零值时间。
func indexedAtTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0)
}
