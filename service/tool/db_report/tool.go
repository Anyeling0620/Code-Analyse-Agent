package db_report

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

type ListTablesInput struct {
	Database string `json:"database,omitempty" jsonschema_description:"可选数据库名；为空时使用配置中的默认数据库或 DSN 当前库"`
}

type DescribeTableInput struct {
	Database string `json:"database,omitempty" jsonschema_description:"可选数据库名；为空时使用配置中的默认数据库或 DSN 当前库"`
	Table    string `json:"table" jsonschema:"required" jsonschema_description:"要查看结构、注释、字段业务含义和关联关系的表名"`
}

type ReadQueryInput struct {
	Query string `json:"query" jsonschema:"required" jsonschema_description:"单条只读 SQL，只允许 SELECT、SHOW、DESCRIBE、DESC、EXPLAIN；禁止任何写入、DDL、权限和存储过程语句"`
}

func NewTools(reporter *DBReport) ([]tool.BaseTool, error) {
	if reporter == nil {
		return nil, errors.New("reporter is nil")
	}
	ListTablesTool, err := toolutils.InferTool(
		"db_list_tables",
		"只读列出数据库中的表、表注释、表类型和估算行数。生成报表前应先用它确认可用业务表。",
		func(ctx context.Context, input ListTablesInput) (string, error) {
			return reporter.ListTables(ctx, input)
		},
	)
	if err != nil {
		return nil, err
	}
	describeTableTool, err := toolutils.InferTool(
		"db_describe_table",
		"只读查看表结构、表注释、字段注释、主键/索引标记和外键关联。写 SQL 前必须先用它理解相关表和字段业务含义。",
		func(ctx context.Context, input DescribeTableInput) (string, error) {
			return reporter.DescribeTable(ctx, input)
		},
	)
	if err != nil {
		return nil, err
	}
	readQueryTool, err := toolutils.InferTool(
		"db_read_query",
		"执行单条只读 SQL 并返回 Markdown 表格。仅用于 SELECT、SHOW、DESCRIBE、DESC、EXPLAIN，不允许写入或修改数据库。",
		func(ctx context.Context, input ReadQueryInput) (string, error) {
			return reporter.ReadQuery(ctx, input)
		},
	)
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{ListTablesTool, describeTableTool, readQueryTool}, nil
}
