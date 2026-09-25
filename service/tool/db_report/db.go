package db_report

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

type DBReport struct {
	driver          string
	dsn             string
	defaultDatabase string
	maxRows         int
	maxCellRunes    int
	queryTimeoutSec time.Duration

	mu sync.Mutex
	db *sql.DB
}

func NewDBReport(conf config.DatabaseReport) (*DBReport, error) {
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
	return &DBReport{
		driver:          driver,
		dsn:             dsn,
		defaultDatabase: conf.Database,
		maxRows:         maxRows,
		maxCellRunes:    maxCellRunes,
		queryTimeoutSec: time.Duration(timeoutSec) * time.Second,
		mu:              sync.Mutex{},
	}, nil
}

func (r *DBReport) getDB() (*sql.DB, error) {
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

func (r *DBReport) Close() error {
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
func (r *DBReport) ListTables(ctx context.Context, input ListTablesInput) (string, error) {
	database, err := r.resolveDatabase(ctx, input.Database)
	if err != nil {
		return "", err
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
		return "", err
	}
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "database=%s tables=%d\n\n", database, len(rows.rows))
	b.WriteString(markdownTable(rows.headers, rows.rows, r.maxCellRunes))
	return b.String(), nil
}

// DescribeTable 2. 获取这些表有哪些字段，表示含义
func (r *DBReport) DescribeTable(ctx context.Context, input DescribeTableInput) (string, error) {
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
func (r *DBReport) ReadQuery(ctx context.Context, input ReadQueryInput) (string, error) {
	if prohibitedSQLTokenRE.MatchString(input.Query) {
		return "query contains non-read-only keyword, denied", nil
	}
	return r.readQuery(ctx, input.Query, "query")
}

func (r *DBReport) readQuery(ctx context.Context, query, label string) (string, error) {
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

func (r *DBReport) resolveDatabase(ctx context.Context, inputDBName string) (string, error) {
	if inputDBName != "" {
		return inputDBName, nil
	}
	if r.defaultDatabase != "" {
		return r.defaultDatabase, nil
	}
	result, err := r.queryRows(ctx, "SELECT DATABASE() AS database_name")
	if err != nil {
		return "", err
	}
	if len(result.rows) == 0 || len(result.rows[0]) == 0 {
		return "", errors.New("database not found")
	}
	return result.rows[0][0], nil
}

type queryResult struct {
	headers []string
	rows    [][]string
}

func (r *DBReport) queryRows(ctx context.Context, query string, args ...any) (queryResult, error) {
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
