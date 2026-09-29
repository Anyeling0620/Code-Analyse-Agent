package project_qa

import (
	"context"
	"edu.agent.code/service/agent"
	"edu.agent.code/service/agent/force_answer"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
)

const (
	Name = "project_qa"
)

type Options struct {
	MaxIterations int
}

func NewQaAgentWithOptions(
	ctx context.Context,
	chatModel model.ToolCallingChatModel,
	qaTools []tool.BaseTool,
	toolMiddlewares []compose.ToolMiddleware,
	opts Options) (adk.Agent, error) {
	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        Name,
		Description: "基于当前项目代码、文档和会话上下文回答项目实现问题，适合连续追问、代码定位、模块解释和调用链说明。",
		Model:       chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               qaTools,
				ExecuteSequentially: true,
				ToolCallMiddlewares: toolMiddlewares,
			},
			EmitInternalEvents: true,
		},
		GenModelInput: agent.GetGenModelInputFunc(projectQAChatTemplate),
		MaxIterations: opts.MaxIterations,
		// 因为要强制出报告 需要做一个中间件强制出
		Handlers: []adk.ChatModelAgentMiddleware{
			force_answer.NewForceAnswerHandler(force_answer.Config{
				MaxIterations: opts.MaxIterations,
				ActiveKey:     projectQAForceAnswerActiveKey,
				Instruction:   projectQAForceAnswerInstruction,
			}),
		},
	})
}
