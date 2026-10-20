package self_report

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
	Name = "self_report"
)

type Options struct {
	MaxIterations int
}

// NewSelfReportAgentWithOptions 构造自省报表子 agent。
//
// 它单独占一个子 agent，而不是把自省工具直接挂到主 agent 上：
// 自省问题（"agent 自己跑得怎么样"）与项目分析、问答的性质完全不同，
// 单独拆开后工具说明和输出口径都能写得更准，也不会污染主 agent 的选路。
// 自省工具只在 self_report.enable 时才被挂载，未启用时模型完全看不到。
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
			// AgentTool 转发完整历史时会过滤 system 消息，画像必须由中间件重新注入。
			userprofile.NewMiddleware(),
			force_answer.NewForceAnswerHandler(force_answer.Config{
				MaxIterations: opts.MaxIterations,
				ActiveKey:     selfReportForceAnswerActiveKey,
				Instruction:   selfReportForceAnswerInstruction,
			}),
		},
	})
}
