package runner

import (
	"context"
	"edu.agent.code/service/agent"
	"edu.agent.code/service/agent/force_answer"
	"edu.agent.code/service/agent/project_qa"
	"edu.agent.code/service/agent/repo_analyzer"
	"edu.agent.code/service/agent/self_report"
	"edu.agent.code/service/agent/userprofile"
	"edu.agent.code/service/consts"
	"fmt"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
)

const (
	ComposeAgentName      = "edu_agent_code_compose"
	RepoAnalyzerAgentName = repo_analyzer.Name
)

type IterationLimits struct {
	Compose              int
	ProjectQA            int
	RepoAnalyzer         int
	RepoAnalyzerSubAgent int
	SelfReport           int
}

type ComposeRunner struct {
	ctx             context.Context
	chatModel       model.ToolCallingChatModel
	directTool      []tool.BaseTool
	analysisTool    []tool.BaseTool
	qaTool          []tool.BaseTool
	selfReportTool  []tool.BaseTool
	ragRegister     tool.BaseTool
	checkPointStore adk.CheckPointStore
	toolMiddleWare  []compose.ToolMiddleware
	agentHandler    []adk.ChatModelAgentMiddleware
	maxIterations   IterationLimits
}

func NewComposeRunner() *ComposeRunner {
	return &ComposeRunner{
		ctx: context.Background(),
	}
}

func (c *ComposeRunner) WithChatModel(chatModel model.ToolCallingChatModel) *ComposeRunner {
	c.chatModel = chatModel
	return c
}

func (c *ComposeRunner) WithDirectTool(tools []tool.BaseTool) *ComposeRunner {
	c.directTool = tools
	return c
}

func (c *ComposeRunner) WithAnalysisTool(tools []tool.BaseTool) *ComposeRunner {
	c.analysisTool = tools
	return c
}

func (c *ComposeRunner) WithQaTool(tools []tool.BaseTool) *ComposeRunner {
	c.qaTool = tools
	return c
}

func (c *ComposeRunner) WithSelfReportTool(tools []tool.BaseTool) *ComposeRunner {
	c.selfReportTool = tools
	return c
}

func (c *ComposeRunner) WithRagTool(tool tool.BaseTool) *ComposeRunner {
	c.ragRegister = tool
	return c
}

func (c *ComposeRunner) WithCheckPoint(checkPointStore adk.CheckPointStore) *ComposeRunner {
	c.checkPointStore = checkPointStore
	return c
}

func (c *ComposeRunner) WithToolMiddleware(middlewares []compose.ToolMiddleware) *ComposeRunner {
	c.toolMiddleWare = middlewares
	return c
}

func (c *ComposeRunner) WithAgentHandler(handler []adk.ChatModelAgentMiddleware) *ComposeRunner {
	c.agentHandler = handler
	return c
}

func (c *ComposeRunner) WithMaxIterations(maxIterations IterationLimits) *ComposeRunner {
	c.maxIterations = maxIterations
	return c
}

func (c *ComposeRunner) defaultMaxIterations() {
	if c.maxIterations.Compose <= 0 {
		c.maxIterations.Compose = consts.ComposeMaxIterations

	}
	if c.maxIterations.ProjectQA <= 0 {
		c.maxIterations.ProjectQA = consts.ProjectQAMaxIterations
	}
	if c.maxIterations.SelfReport <= 0 {
		c.maxIterations.SelfReport = consts.SelfReportMaxIterations
	}
	if c.maxIterations.RepoAnalyzer <= 0 {
		c.maxIterations.RepoAnalyzer = consts.RepoAnalysisMaxIterations
	}
	if c.maxIterations.RepoAnalyzerSubAgent <= 0 {
		c.maxIterations.RepoAnalyzerSubAgent = consts.RepoAnalysisMaxIterations
	}
}

func (c *ComposeRunner) check() error {
	if c.chatModel == nil {
		return fmt.Errorf("chatModel is nil")
	}
	if c.checkPointStore == nil {
		return fmt.Errorf("checkPointStore is nil")
	}
	if c.qaTool == nil {
		return fmt.Errorf("qaTool is nil")
	}
	if c.analysisTool == nil {
		return fmt.Errorf("analysisTool is nil")
	}
	return nil
}

func (c *ComposeRunner) Build() (*adk.Runner, error) {
	c.defaultMaxIterations()
	err := c.check()
	if err != nil {
		return nil, err
	}
	// 创建 SubAgent 并封装为工具
	repoAnalyzerTool, err := c.buildAnalyzerAgent()
	if err != nil {
		return nil, err
	}
	projectQaTool, err := c.buildProjectQaAgent()
	if err != nil {
		return nil, err
	}
	agentTools := []tool.BaseTool{c.ragRegister, repoAnalyzerTool, projectQaTool}
	returnDirectly := map[string]bool{
		repo_analyzer.Name: true,
		project_qa.Name:    true,
	}
	// 自省报表工具只在 self_report.enable 时才存在。没启用就不挂这个子 agent，
	// 保证模型看不到、也不会去调它，行为与改造前完全一致。
	if len(c.selfReportTool) > 0 {
		selfReportAgentTool, err := c.buildSelfReportAgent()
		if err != nil {
			return nil, err
		}
		agentTools = append(agentTools, selfReportAgentTool)
		returnDirectly[self_report.Name] = true
	}
	agentTools = append(agentTools, c.directTool...)

	// 画像中间件必须挂在主 Agent 上：审批恢复时不会重新跑初始 GenModelInput，
	// 只有每次模型调用前的 BeforeModelRewriteState 能保证用上当前画像。
	//
	// 强制收尾中间件也必须挂在这里。它读的是 ChatModel 节点的 RemainingIterations，
	// 而该节点的 state pre-handler 是"先判断 <=0 才报错，否则减一"，钩子在减一之后才执行：
	// 也就是最后一轮（remaining 从 1 减到 0）仍然会真正调用模型，此时清空工具并要求
	// 模型直接出结论，所以是能生效的。早期版本误判为"remaining==0 时框架直接终止、
	// 中间件来不及"，于是只留了会话层的一句兜底文案，用户始终拿不到最终结论。
	mainHandlers := make([]adk.ChatModelAgentMiddleware, 0, len(c.agentHandler)+2)
	mainHandlers = append(mainHandlers, c.agentHandler...)
	mainHandlers = append(mainHandlers, userprofile.NewMiddleware())
	mainHandlers = append(mainHandlers, force_answer.NewForceAnswerHandler(force_answer.Config{
		MaxIterations: c.maxIterations.Compose,
		ActiveKey:     mainForceAnswerActiveKey,
		Instruction:   mainForceAnswerInstruction,
	}))

	mainAgent, err := adk.NewChatModelAgent(
		c.ctx,
		&adk.ChatModelAgentConfig{
			Name:        ComposeAgentName,
			Description: "通用对话入口，负责调用本机终端命令、HTTP 请求、仓库分析、项目问答或基于上下文直接回答。",
			Model:       c.chatModel,
			ToolsConfig: adk.ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools:               agentTools,
					ExecuteSequentially: true,
					ToolCallMiddlewares: c.toolMiddleWare,
				},
				ReturnDirectly:     returnDirectly,
				EmitInternalEvents: true,
			},
			GenModelInput: agent.GetGenModelInputFunc(mainChatTemplate),
			MaxIterations: c.maxIterations.Compose,
			Handlers:      mainHandlers,
		},
	)
	return adk.NewRunner(c.ctx, adk.RunnerConfig{
		Agent:           mainAgent,
		EnableStreaming: true,
		CheckPointStore: c.checkPointStore,
	}), nil
}

func (c *ComposeRunner) buildAnalyzerAgent() (tool.BaseTool, error) {
	analyzer, err := repo_analyzer.NewAnalyzerWithOptions(
		c.ctx,
		c.chatModel,
		c.analysisTool,
		c.toolMiddleWare,
		repo_analyzer.Options{
			MaxIterations:         c.maxIterations.RepoAnalyzer,
			SubAgentMaxIterations: c.maxIterations.RepoAnalyzerSubAgent,
		},
	)
	if err != nil {
		return nil, err
	}
	return adk.NewAgentTool(c.ctx, analyzer), nil

}

func (c *ComposeRunner) buildProjectQaAgent() (tool.BaseTool, error) {
	projectQA, err := project_qa.NewQaAgentWithOptions(
		c.ctx,
		c.chatModel,
		c.qaTool,
		c.toolMiddleWare,
		project_qa.Options{
			MaxIterations: c.maxIterations.ProjectQA,
		},
	)
	if err != nil {
		return nil, err
	}
	return adk.NewAgentTool(c.ctx, projectQA, adk.WithFullChatHistoryAsInput()), nil
}

func (c *ComposeRunner) buildSelfReportAgent() (tool.BaseTool, error) {
	selfReportAgent, err := self_report.NewSelfReportAgentWithOptions(
		c.ctx,
		c.chatModel,
		c.selfReportTool,
		c.toolMiddleWare,
		self_report.Options{
			MaxIterations: c.maxIterations.SelfReport,
		},
	)
	if err != nil {
		return nil, err
	}
	return adk.NewAgentTool(c.ctx, selfReportAgent, adk.WithFullChatHistoryAsInput()), nil
}
