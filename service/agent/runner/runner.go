package runner

import (
	"context"
	"edu.agent.code/service/agent/project_qa"
	"edu.agent.code/service/agent/repo_analyzer"
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
	DBReport             int
}

type ComposeRunner struct {
	ctx             context.Context
	chatModel       model.ToolCallingChatModel
	directTool      []tool.BaseTool
	analysisTool    []tool.BaseTool
	qaTool          []tool.BaseTool
	reportTool      []tool.BaseTool
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

func (c *ComposeRunner) WithReportTool(tools []tool.BaseTool) *ComposeRunner {
	c.reportTool = tools
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
	if c.maxIterations.DBReport <= 0 {
		c.maxIterations.DBReport = consts.DBReportMaxIterations
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
	if c.reportTool == nil {
		return fmt.Errorf("reportTool is nil")
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
	repoAnalyzerTool, err := c.buildAnalyzerAgent()
	if err != nil {
		return nil, err
	}
	projectQaTool, err := c.buildProjectQaAgent()
	if err != nil {
		return nil, err
	}

	agentTools := []tool.BaseTool{repoAnalyzerTool, projectQaTool}
	returnDirectly := map[string]bool{
		RepoAnalyzerAgentName: true,
	}
	agentTools = append(agentTools, c.directTool...)

	// TODO 这是浅拷贝 可能有风险
	handlers := append([]adk.ChatModelAgentMiddleware{}, c.agentHandler...)

	// TODO 超过最大迭代次数 强制出报告的handler

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
			GenModelInput: genMainModelInputWithHistory,
			MaxIterations: c.maxIterations.Compose,
			Handlers:      handlers,
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
