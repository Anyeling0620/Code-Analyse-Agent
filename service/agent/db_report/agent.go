package db_report

import (
	"context"
	"edu.agent.code/service/agent"
	"edu.agent.code/service/agent/force_answer"
	"edu.agent.code/service/agent/userprofile"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
)

const (
	Name = "db_report"
)

type Options struct {
	MaxIterations int
}

func NewDBReportAgentWithOptions(
	ctx context.Context,
	chatModel model.ToolCallingChatModel,
	reportTools []tool.BaseTool,
	toolMiddlewares []compose.ToolMiddleware,
	opts Options) (adk.Agent, error) {
	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        Name,
		Description: "基于数据库表注释、字段注释和只读 SQL 查询生成 Markdown 报表，适合经营分析、数据盘点、趋势统计和明细清单，各类数据查询等需求场景。",
		Model:       chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               reportTools,
				ExecuteSequentially: true,
				ToolCallMiddlewares: toolMiddlewares,
			},
			EmitInternalEvents: true,
		},
		GenModelInput: agent.GetGenModelInputFunc(dbReportChatTemplate),
		MaxIterations: opts.MaxIterations,
		// 因为要强制出报告 需要做一个中间件强制出
		Handlers: []adk.ChatModelAgentMiddleware{
			// AgentTool 转发完整历史时会过滤 system 消息，画像必须由中间件重新注入。
			userprofile.NewMiddleware(),
			force_answer.NewForceAnswerHandler(force_answer.Config{
				MaxIterations: opts.MaxIterations,
				ActiveKey:     dbReportForceAnswerActiveKey,
				Instruction:   dbReportForceAnswerInstruction,
			}),
		},
	})
}
