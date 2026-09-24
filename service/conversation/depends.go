package conversation

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/approval"
	"edu.agent.code/adaptor/repo/checkpoint"
	"edu.agent.code/adaptor/repo/profile"
	"edu.agent.code/adaptor/repo/session"
	"edu.agent.code/config"
	"edu.agent.code/service/agent/runner"
	"edu.agent.code/service/cost"
	"edu.agent.code/service/rag"
	"edu.agent.code/utils/dsml"
	"edu.agent.code/utils/logger"
	"fmt"
	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"time"
)

type conversationTools struct {
	analysis []tool.BaseTool
	direct   []tool.BaseTool
	qa       []tool.BaseTool
	report   []tool.BaseTool
}

type serviceRepos struct {
	approvals       approval.IApproval
	checkPointStore adk.CheckPointStore
	profiles        profile.IProfile
	sessions        session.ISession
}

type serviceDeps struct {
	tools         conversationTools
	repo          serviceRepos
	composeRunner *adk.Runner
	visibleTools  map[string]bool
	cost          *cost.Service
	rag           *rag.Service
}

func buildServiceDeps(ctx context.Context, a adaptor.IAdaptor) (deps serviceDeps, err error) {
	// TODO tools handlers ragTool
	conf := a.GetConfig()
	chatModel, err := buildChatModel(ctx, a.GetConfig())
	if err != nil {
		return serviceDeps{}, err
	}

	agentHandlers := []adk.ChatModelAgentMiddleware{}

	tools := conversationTools{}
	repo := buildRepo(a)
	composeRunner, err := buildComposeRunner(
		chatModel,
		tools,
		repo,
		conf,
		agentHandlers,
		nil,
	)
	if err != nil {
		logger.Error("buildComposeRunner err:", err)
		return serviceDeps{}, err
	}
	return serviceDeps{
		tools:         tools,
		repo:          repo,
		composeRunner: composeRunner,
		visibleTools:  nil,
		cost:          cost.NewService(a),
		rag:           nil,
	}, nil
}

func buildRepo(adaptor adaptor.IAdaptor) serviceRepos {
	return serviceRepos{
		approvals:       approval.NewApproval(adaptor),
		profiles:        profile.NewProfile(adaptor),
		sessions:        session.NewSession(adaptor),
		checkPointStore: checkpoint.NewCheckPoint(adaptor),
	}
}

func buildComposeRunner(chatModel model.ToolCallingChatModel,
	tools conversationTools,
	repo serviceRepos,
	conf *config.Config,
	agentHandler []adk.ChatModelAgentMiddleware,
	ragTool tool.BaseTool,
) (*adk.Runner, error) {
	composeRunner, err := runner.NewComposeRunner().
		WithChatModel(chatModel).
		WithAnalysisTool(tools.analysis).
		WithQaTool(tools.qa).
		WithDirectTool(tools.direct).
		WithReportTool(tools.report).
		WithCheckPoint(repo.checkPointStore).
		WithMaxIterations(buildIterationLimits(conf.Agents.MazIterations)).
		WithAgentHandler(agentHandler).
		WithRagTool(ragTool).Build()

	if err != nil {
		logger.Error("build compose runner error:", err)
		return nil, fmt.Errorf("build compose runner error: %w", err)
	}

	return composeRunner, nil
}

func buildIterationLimits(conf config.AgentMaxIterations) runner.IterationLimits {
	return runner.IterationLimits{
		Compose:              conf.Compose,
		ProjectQA:            conf.ProjectQA,
		RepoAnalyzer:         conf.ReportAnalyzer,
		RepoAnalyzerSubAgent: conf.RepAnalyzerSubAgent,
		DBReport:             conf.DBReport,
	}
}

func buildChatModel(ctx context.Context, conf *config.Config) (model.ToolCallingChatModel, error) {
	baseModel, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		APIKey:  conf.DeepSeek.APIKey,
		Timeout: time.Minute * 5,
		BaseURL: conf.DeepSeek.BaseURL,
		Model:   conf.DeepSeek.Model,
	})
	if err != nil {
		logger.Error("buildChatModel NewChatModel err:%v", err)
		return nil, err
	}
	return dsml.WrapEinoModel(baseModel), nil
}
