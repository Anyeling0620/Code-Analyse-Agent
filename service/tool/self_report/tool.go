package self_report

import (
	"context"
	"edu.agent.code/common"
	"edu.agent.code/service/tool/db_report"
	"errors"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

// noPermissionMessage 是白名单外的用户调用自省工具时返回的结果。
//
// 它必须是**普通字符串结果**而不是 Go error：本项目给模型的工具都是
// (string, error)，返回 error 会让 eino 把它当工具执行失败，甚至中断本轮；
// 返回一句明确的拒绝说明，模型才知道该停止尝试并告知用户。
const noPermissionMessage = "无权限：agent 运行指标（自省报表）只对白名单用户开放，请改用其他方式回答。"

// NewTools 构造自省报表的三个只读工具。
//
// 与 db_report 的工具是两套独立实例，连接的是另一个库、另一个只读账号：
//   - db_report 连业务库（共享只读账号，所有用户可用）；
//   - self_report 连 agent_telemetry（独立只读账号 + 白名单，只给管理用户）。
//
// 这里不做"注册时按用户过滤工具"——工具集是服务启动时全局构建一次的
// （见 service/conversation/depends.go 的 buildComposeRunner），
// 没有随用户变化的挂载点，所以门禁只能放在**工具执行时**按 ctx 里的用户判定。
func NewTools(reporter *db_report.DBReport, allowedUsers []string) ([]tool.BaseTool, error) {
	if reporter == nil {
		return nil, errors.New("self report reporter is nil")
	}
	allow := buildAllowSet(allowedUsers)

	listTablesTool, err := toolutils.InferTool(
		"obs_list_tables",
		"只读列出 agent 运行指标库（agent_telemetry）的表、表注释与估算行数。"+
			"仅白名单用户可用；用于确认自省报表可查的数据表。",
		func(ctx context.Context, input db_report.ListTablesInput) (string, error) {
			if !allowed(ctx, allow) {
				return noPermissionMessage, nil
			}
			return reporter.ListTables(ctx, input)
		},
	)
	if err != nil {
		return nil, err
	}

	describeTableTool, err := toolutils.InferTool(
		"obs_describe_table",
		"只读查看 agent 运行指标库某张表的结构、字段注释与索引。"+
			"仅白名单用户可用；写自省 SQL 前应先用它理解字段口径。",
		func(ctx context.Context, input db_report.DescribeTableInput) (string, error) {
			if !allowed(ctx, allow) {
				return noPermissionMessage, nil
			}
			return reporter.DescribeTable(ctx, input)
		},
	)
	if err != nil {
		return nil, err
	}

	readQueryTool, err := toolutils.InferTool(
		"obs_read_query",
		"对 agent 运行指标库执行单条只读 SQL 并返回 Markdown 表格。"+
			"仅白名单用户可用；只允许 SELECT/SHOW/DESCRIBE/DESC/EXPLAIN。",
		func(ctx context.Context, input db_report.ReadQueryInput) (string, error) {
			if !allowed(ctx, allow) {
				return noPermissionMessage, nil
			}
			return reporter.ReadQuery(ctx, input)
		},
	)
	if err != nil {
		return nil, err
	}

	return []tool.BaseTool{listTablesTool, describeTableTool, readQueryTool}, nil
}

// buildAllowSet 把白名单配置转成集合。
// 空白名单返回空集合 —— 即"谁都不放行"，而不是"全都放行"：
// 漏配 allowed_users 时最坏的结果应该是查不到，而不是全员可见。
func buildAllowSet(allowedUsers []string) map[string]struct{} {
	allow := make(map[string]struct{}, len(allowedUsers))
	for _, u := range allowedUsers {
		if u != "" {
			allow[u] = struct{}{}
		}
	}
	return allow
}

// allowed 判断当前会话的用户是否在白名单内。
// ctx 里的用户由 run 启动时写入（common.WithUserAndSession），工具层拿不到 plan，
// 所以这里只做显式用户白名单，不猜套餐。
func allowed(ctx context.Context, allow map[string]struct{}) bool {
	if len(allow) == 0 {
		return false
	}
	userID := common.UserIDFromContext(ctx)
	if userID == "" {
		return false
	}
	_, ok := allow[userID]
	return ok
}
