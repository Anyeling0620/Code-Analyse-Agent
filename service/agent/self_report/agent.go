package self_report

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
	Name = "self_report"
)

type Options struct {
	MaxIterations int
}

// NewSelfReportAgentWithOptions 构造自省报表子 agent。
//
// 它与 db_report 是并列的两个子 agent，而不是同一个 agent 的两组工具：
// 两者面向的问题完全不同（"业务经营数据怎么样" vs "agent 自己跑得怎么样"），
// 混在一个 prompt 里会让模型在两边之间乱选库；拆开后各自的工具说明和
// 输出口径都能写得更准。
//
// 能查到什么由工具层白名单决定（见 service/tool/self_report），
// 这里不做权限判断——prompt 不是权限边界。
func NewSelfReportAgentWithOptions(
	ctx context.Context,
	chatModel model.ToolCallingChatModel,
	selfReportTools []tool.BaseTool,
	toolMiddlewares []compose.ToolMiddleware,
	opts Options) (adk.Agent, error) {
	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        Name,
		Description: "基于 agent 自身运行指标（每轮 run 的迭代轮次、工具调用与失败、token 与成本、降级/中断原因）做只读查询并生成中文 Markdown 自省报表，用于定位该调配置还是该修 bug。",
		Model:       chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               selfReportTools,
				ExecuteSequentially: true,
				ToolCallMiddlewares: toolMiddlewares,
			},
			EmitInternalEvents: true,
		},
		GenModelInput: agent.GetGenModelInputFunc(selfReportChatTemplate),
		MaxIterations: opts.MaxIterations,
		Handlers: []adk.ChatModelAgentMiddleware{
			force_answer.NewForceAnswerHandler(force_answer.Config{
				MaxIterations: opts.MaxIterations,
				ActiveKey:     selfReportForceAnswerActiveKey,
				Instruction:   selfReportForceAnswerInstruction,
			}),
		},
	})
}
