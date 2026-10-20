package self_report

import (
	"context"
	"database/sql"
	"edu.agent.code/config"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultDriver             = "mysql"
	defaultMaxRows            = 100
	defaultMaxCellRunes       = 200
	defaultMaxQueryTimeoutSec = 30
	maxQueryRunes             = 20000
)

var prohibitedSQLTokenRE = regexp.MustCompile(`(?i)\b(insert|update|delete|replace|alter|drop|create|truncate|grant|revoke|call|set|use|lock|unlock|load|outfile|dumpfile|procedure|function|trigger|event|index|analyze|optimize|repair|flush|reset|kill|handler|do)\b`)

// Reader 是 self_report 的只读查询引擎，指向 agent_telemetry 运行指标库。
//
// 它只做三件事：列库表、看表结构、执行单条只读 SQL，并把结果转成
// Markdown 表格。写操作由 forbidden 关键字校验 + 只读账号双重拦截，
// 结果大小由 maxRows / maxCellRunes / queryTimeoutSec 限制。
type Reader struct {
	driver          string
	dsn             string
	defaultDatabase string
	maxRows         int
	maxCellRunes    int
	queryTimeoutSec time.Duration

	mu sync.Mutex
	db *sql.DB
}

// NewReader 按 self_report 配置构造只读查询引擎。
//
// dsn 为空直接报错；driver、max_rows、max_cell_runes、query_timeout_sec
// 缺省时落到包内默认值，保证配置漏填也不会退化成无限制查询。
func NewReader(conf config.SelfReport) (*Reader, error) {
	if conf.DSN == "" {
		return nil, errors.New("dsn is empty")
	}
	dsn := conf.DSN
	driver := conf.Driver
	if driver == "" {
		driver = defaultDriver
	}
	maxRows := conf.MaxRows
	if maxRows <= 0 {
		maxRows = defaultMaxRows
	}
	maxCellRunes := conf.MaxCellRunes
	if maxCellRunes <= 0 {
		maxCellRunes = defaultMaxCellRunes
	}
	// TODO 可能是对的
	if maxCellRunes > maxQueryRunes {
		maxCellRunes = maxQueryRunes
	}
	timeoutSec := conf.QueryTimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = defaultMaxQueryTimeoutSec
	}
	return &Reader{
		driver:          driver,
		dsn:             dsn,
		defaultDatabase: conf.Database,
		maxRows:         maxRows,
		maxCellRunes:    maxCellRunes,
		queryTimeoutSec: time.Duration(timeoutSec) * time.Second,
		mu:              sync.Mutex{},
	}, nil
}

func (r *Reader) getDB() (*sql.DB, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.db != nil {
		return r.db, nil
	}
	db, err := sql.Open(r.driver, r.dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	r.db = db
	return db, nil
}

func (r *Reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer func() {
		r.db = nil
	}()
	if r.db != nil {
		return r.db.Close()
	}
	return nil
}

// ListTables 1.获取有什么表
func (r *Reader) ListTables(ctx context.Context, input ListTablesInput) (string, error) {
	database, err := r.resolveDatabase(ctx, input.Database)
	if err != nil {
		return err.Error(), nil
	}
	// 需要知道引擎？
	query := `SELECT TABLE_NAME AS table_name,
             		 TABLE_TYPE AS table_type, 
					 ENGINE AS engine,
					 TABLE_COMMENT AS comment,
					 TABLE_ROWS AS rows_estimate
			  FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`
	rows, err := r.queryRows(ctx, query, database)
	if err != nil {
		return err.Error(), nil
	}
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "database=%s tables=%d\n\n", database, len(rows.rows))
	b.WriteString(markdownTable(rows.headers, rows.rows, r.maxCellRunes))
	return b.String(), nil
}

// DescribeTable 2. 获取这些表有哪些字段，表示含义
func (r *Reader) DescribeTable(ctx context.Context, input DescribeTableInput) (string, error) {
	table := strings.TrimSpace(input.Table)
	if table == "" {
		return "table cannot be empty", nil
	}
	database, err := r.resolveDatabase(ctx, input.Database)
	if err != nil {
		return err.Error(), nil
	}

	tableRows, err := r.queryRows(ctx, `SELECT TABLE_NAME AS table_name, ENGINE AS engine, TABLE_ROWS AS rows_estimate, TABLE_COMMENT AS comment
FROM information_schema.TABLES
WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, database, table)
	if err != nil {
		return err.Error(), nil
	}
	if len(tableRows.rows) == 0 {
		return fmt.Sprintf("table %s.%s does not exist", database, table), nil
	}

	columnRows, err := r.queryRows(ctx, `SELECT ORDINAL_POSITION AS ordinal, COLUMN_NAME AS column_name, COLUMN_TYPE AS column_type,
IS_NULLABLE AS nullable, COLUMN_KEY AS column_key, COLUMN_DEFAULT AS column_default, EXTRA AS extra, COLUMN_COMMENT AS comment
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
ORDER BY ORDINAL_POSITION`, database, table)
	if err != nil {
		return err.Error(), nil
	}
	indexRows, err := r.queryRows(ctx, `SELECT INDEX_NAME AS index_name, NON_UNIQUE AS non_unique, SEQ_IN_INDEX AS seq_in_index,
COLUMN_NAME AS column_name, INDEX_TYPE AS index_type
FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
ORDER BY INDEX_NAME, SEQ_IN_INDEX`, database, table)
	if err != nil {
		return err.Error(), nil
	}

	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "database=%s table=%s\n\n", database, table)
	b.WriteString("--- table ---\n")
	b.WriteString(markdownTable(tableRows.headers, tableRows.rows, r.maxCellRunes))
	b.WriteString("\n\n--- columns ---\n")
	b.WriteString(markdownTable(columnRows.headers, columnRows.rows, r.maxCellRunes))
	b.WriteString("\n\n--- indexes ---\n")
	b.WriteString(markdownTable(indexRows.headers, indexRows.rows, r.maxCellRunes))
	return b.String(), nil
}

// ReadQuery 3. sql执行，返回执行结果
func (r *Reader) ReadQuery(ctx context.Context, input ReadQueryInput) (string, error) {
	if prohibitedSQLTokenRE.MatchString(input.Query) {
		return "query contains non-read-only keyword, denied", nil
	}
	return r.readQuery(ctx, input.Query, "query")
}

func (r *Reader) readQuery(ctx context.Context, query, label string) (string, error) {
	db, err := r.getDB()
	if err != nil {
		return err.Error(), nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, r.queryTimeoutSec)
	defer cancel()

	rows, err := db.QueryContext(queryCtx, query)
	if err != nil {
		return err.Error(), nil
	}
	defer rows.Close()

	headers, err := rows.Columns()
	if err != nil {
		return err.Error(), nil
	}
	values, truncated, err := scanRows(rows, r.maxRows)
	if err != nil {
		return err.Error(), nil
	}
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "%s rows=%d truncated=%t max_rows=%d\n\n", label, len(values), truncated, r.maxRows)
	b.WriteString(markdownTable(headers, values, r.maxCellRunes))
	return b.String(), nil
}

func (r *Reader) resolveDatabase(ctx context.Context, inputDBName string) (string, error) {
	if inputDBName != "" {
		return inputDBName, nil
	}
	if r.defaultDatabase != "" {
		return r.defaultDatabase, nil
	}
	result, err := r.queryRows(ctx, "SELECT DATABASE() AS database_name")
	if err != nil {
		return err.Error(), nil
	}
	if len(result.rows) == 0 || len(result.rows[0]) == 0 {
		return "Database not found", nil
	}
	return result.rows[0][0], nil
}

type queryResult struct {
	headers []string
	rows    [][]string
}

func (r *Reader) queryRows(ctx context.Context, query string, args ...any) (queryResult, error) {
	db, err := r.getDB()
	if err != nil {
		return queryResult{}, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, r.queryTimeoutSec)
	defer cancel()

	rows, err := db.QueryContext(queryCtx, query, args...)
	if err != nil {
		return queryResult{}, err
	}
	defer rows.Close()

	headers, err := rows.Columns()
	if err != nil {
		return queryResult{}, err
	}

	values, _, err := scanRows(rows, r.maxRows)
	if err != nil {
		return queryResult{}, err
	}
	return queryResult{
		headers: headers,
		rows:    values,
	}, nil
}

func scanRows(rows *sql.Rows, maxRows int) ([][]string, bool, error) {
	var result [][]string
	truncated := false
	headers, err := rows.Columns()
	if err != nil {
		return nil, false, err
	}
	for rows.Next() {
		if len(result) >= maxRows {
			truncated = true
			break
		}
		raw := make([]sql.RawBytes, len(headers))
		dest := make([]any, len(headers))
		for i := range raw {
			dest[i] = &raw[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, false, err
		}
		row := make([]string, len(headers))
		for i, value := range raw {
			if value == nil {
				row[i] = "NULL"
				continue
			}
			row[i] = string(value)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return result, truncated, nil
}

func markdownTable(headers []string, rows [][]string, maxCellRunes int) string {
	if len(headers) == 0 {
		return "(no columns)"
	}
	var b strings.Builder
	writeMarkdownRow(&b, headers, maxCellRunes)
	separator := make([]string, len(headers))
	for i := range separator {
		separator[i] = "---"
	}
	writeMarkdownRow(&b, separator, maxCellRunes)
	for _, row := range rows {
		cells := make([]string, len(headers))
		for i := range headers {
			if i < len(row) {
				cells[i] = row[i]
			}
		}
		writeMarkdownRow(&b, cells, maxCellRunes)
	}
	if len(rows) == 0 {
		b.WriteString("\n(no rows)")
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeMarkdownRow(b *strings.Builder, cells []string, maxCellRunes int) {
	b.WriteString("|")
	for _, cell := range cells {
		b.WriteString(" ")
		b.WriteString(formatMarkdownCell(cell, maxCellRunes))
		b.WriteString(" |")
	}
	b.WriteString("\n")
}

func formatMarkdownCell(value string, maxCellRunes int) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.ReplaceAll(value, "\n", "<br>")
	value = strings.ReplaceAll(value, "|", `\|`)
	if maxCellRunes <= 0 {
		maxCellRunes = defaultMaxCellRunes
	}
	runes := []rune(value)
	if len(runes) <= maxCellRunes {
		return value
	}
	return string(runes[:maxCellRunes]) + "...<truncated:" + strconv.Itoa(len(runes)) + ">"
}
