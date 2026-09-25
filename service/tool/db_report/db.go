package db_report

import (
	"context"
	"database/sql"
	"edu.agent.code/config"
	"errors"
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

// 1.获取有什么表
func (r *DBReport) ListTables(ctx context.Context, input ListTablesInput) (string, error) {
	return "", nil
}

// 2.获取这些表有哪些字段 表示含义
func (r *DBReport) DescribeTable(ctx context.Context, input DescribeTableInput) (string, error) {
	return "", nil
}

// 3.sql执行 返回执行结果
func (r *DBReport) ReadQuery(ctx context.Context, input ReadQueryInput) (string, error) {
	return "", nil
}
