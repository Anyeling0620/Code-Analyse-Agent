package runner

import (
	"context"
	"edu.agent.code/service/agent"
	"edu.agent.code/service/agent/db_report"
	"edu.agent.code/service/agent/project_qa"
	"edu.agent.code/service/agent/repo_analyzer"
	"edu.agent.code/service/agent/self_report"
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
	SelfReport           int
}

type ComposeRunner struct {
	ctx             context.Context
	chatModel       model.ToolCallingChatModel
	directTool      []tool.BaseTool
	analysisTool    []tool.BaseTool
	qaTool          []tool.BaseTool
	reportTool      []tool.BaseTool
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

func (c *ComposeRunner) WithReportTool(tools []tool.BaseTool) *ComposeRunner {
	c.reportTool = tools
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
	if c.maxIterations.DBReport <= 0 {
		c.maxIterations.DBReport = consts.DBReportMaxIterations
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
	// 创建 SubAgent 并封装为工具
	repoAnalyzerTool, err := c.buildAnalyzerAgent()
	if err != nil {
		return nil, err
	}
	projectQaTool, err := c.buildProjectQaAgent()
	if err != nil {
		return nil, err
	}
	dbReportTool, err := c.buildDBReportAgent()
	if err != nil {
		return nil, err
	}

	agentTools := []tool.BaseTool{c.ragRegister, repoAnalyzerTool, projectQaTool, dbReportTool}
	returnDirectly := map[string]bool{
		repo_analyzer.Name: true,
		project_qa.Name:    true,
		db_report.Name:     true,
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

	// 主 Agent 不加"最后一轮强制出报告"的中间件：eino 的 chat-model 前置钩子在
	// remaining <= 0 时会先返回 ErrExceedMaxIterations，中间件来不及改写提示词。
	// 超过最大迭代次数的收尾统一由 conversation.consumeAgentEvents 的错误分支兜底
	// （见 handleMaxIterationsExceeded：把已产出内容当部分报告下发，而不是报错）。

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
			Handlers:      c.agentHandler,
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

func (c *ComposeRunner) buildDBReportAgent() (tool.BaseTool, error) {
	dbReport, err := db_report.NewDBReportAgentWithOptions(
		c.ctx,
		c.chatModel,
		c.reportTool,
		c.toolMiddleWare,
		db_report.Options{
			MaxIterations: c.maxIterations.DBReport,
		},
	)
	if err != nil {
		return nil, err
	}
	return adk.NewAgentTool(c.ctx, dbReport, adk.WithFullChatHistoryAsInput()), nil
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
