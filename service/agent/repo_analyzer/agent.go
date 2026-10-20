package repo_analyzer

import (
	"context"
	"edu.agent.code/service/agent/force_answer"
	"edu.agent.code/service/agent/userprofile"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
)

const Name = "repo_analyzer"

type Options struct {
	MaxIterations         int
	SubAgentMaxIterations int
}

func NewAnalyzerWithOptions(
	ctx context.Context,
	chatModel model.ToolCallingChatModel,
	analysisTools []tool.BaseTool,
	toolMiddlewares []compose.ToolMiddleware,
	opts Options) (adk.Agent, error) {
	subAgents, err := newRepoAnalyzerSubAgent(ctx, chatModel, analysisTools, opts.SubAgentMaxIterations, toolMiddlewares)
	if err != nil {
		return nil, err
	}
	return deep.New(ctx, &deep.Config{
		Name:        Name,
		Description: "深度分析本机或远端代码仓库：动态扫描目录、读取关键文件、识别技术栈和调用链路，并输出完整中文项目分析报告。",
		ChatModel:   chatModel,
		Instruction: repoAnalyzerInstruction,
		SubAgents:   subAgents,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               analysisTools,
				ExecuteSequentially: true,
				ToolCallMiddlewares: toolMiddlewares,
			},
			EmitInternalEvents: true,
		},
		WithoutGeneralSubAgent:       true,
		MaxIteration:                 opts.MaxIterations,
		TaskToolDescriptionGenerator: repoAnalyzerTaskToolDescription,
		Handlers: []adk.ChatModelAgentMiddleware{
			// 画像注入：AgentTool 转发历史时会过滤 system 消息，必须由中间件重新补上。
			userprofile.NewMiddleware(),
			force_answer.NewForceAnswerHandler(force_answer.Config{
				MaxIterations: opts.MaxIterations,
				ActiveKey:     repoAnalyzerForceReportActiveKey,
				Instruction:   repoAnalyzerForceReportInstruction,
			}),
		},
	})
}

type subAgentSpec struct {
	name        string
	description string
	instruction string
}

// repoAnalyzerSubAgents 是 repo_analyzer 的二级子 Agent 定义。
//
// 同一份列表既用于构造 Agent，也通过 SubAgentNames 暴露给会话层：
// 这些 Agent 的 assistant 正文是内部中间结论，只回传给 repo_analyzer 汇总，
// 不能作为用户可见的回答下发（否则一次分析会出现多份“总结”）。
var repoAnalyzerSubAgents = []subAgentSpec{
	{
		name:        "repo_structure_mapper",
		description: "专注确认仓库技术栈、目录分层、入口文件、构建脚本和关键依赖，适合先建立项目全景地图。",
		instruction: repoStructureMapperInstruction,
	},
	{
		name:        "repo_callchain_analyzer",
		description: "专注分析后端路由、服务层、工具调用、Agent/Runner 调用链路和关键业务流程。",
		instruction: repoCallchainAnalyzerInstruction,
	},
	{
		name:        "repo_data_config_analyzer",
		description: "专注分析配置、数据库模型、Repository、状态持久化、中间件和安全边界。",
		instruction: repoDataConfigAnalyzerInstruction,
	},
	{
		name:        "repo_frontend_analyzer",
		description: "专注分析前端页面、SSE/HTTP 交互、用户操作流和前后端事件协议。",
		instruction: repoFrontendAnalyzerInstruction,
	},
}

// SubAgentNames 返回 repo_analyzer 的二级子 Agent 名称。
//
// 调用方据此区分“给 repo_analyzer 的证据”和“面向用户的最终报告”。
func SubAgentNames() []string {
	names := make([]string, 0, len(repoAnalyzerSubAgents))
	for _, spec := range repoAnalyzerSubAgents {
		names = append(names, spec.name)
	}
	return names
}

func newRepoAnalyzerSubAgent(
	ctx context.Context,
	chatModel model.ToolCallingChatModel,
	analysisTools []tool.BaseTool,
	maxIterations int,
	toolMiddlewares []compose.ToolMiddleware) ([]adk.Agent, error) {
	subAgents := make([]adk.Agent, 0, len(repoAnalyzerSubAgents))
	for _, config := range repoAnalyzerSubAgents {
		agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name:        config.name,
			Description: config.description,
			Instruction: config.instruction,
			Model:       chatModel,
			ToolsConfig: adk.ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools:               analysisTools,
					ExecuteSequentially: true,
					ToolCallMiddlewares: toolMiddlewares,
				},
				EmitInternalEvents: true,
			},
			MaxIterations: maxIterations,
			// 二级子 Agent 只拿到任务描述，历史里的画像 system 消息不会跟过来，
			// 同样依赖中间件从 context 注入当前画像。
			Handlers: []adk.ChatModelAgentMiddleware{
				userprofile.NewMiddleware(),
			},
		})
		if err != nil {
			return nil, err
		}
		subAgents = append(subAgents, agent)
	}
	return subAgents, nil
}
