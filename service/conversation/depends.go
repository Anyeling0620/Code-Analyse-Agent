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
	"edu.agent.code/service/tool/provider"
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
	toolProvider  provider.IProvider
	composeRunner *adk.Runner
	visibleTools  map[string]bool
	cost          *cost.Service
	rag           *rag.Service
}

func buildServiceDeps(ctx context.Context, a adaptor.IAdaptor) (deps serviceDeps, err error) {
	// TODO tools handlers ragTool
	conf := a.GetConfig()
	// 处理工具集
	toolProvider := provider.NewProvider(a)
	toolGroups, err := toolProvider.Load(ctx)
	if err != nil {
		logger.Error("loading tool provider err", err)
		return serviceDeps{}, err
	}
	defer func() {
		if err != nil {
			err = toolProvider.Close()
			if err != nil {
				logger.Error("closing tool provider err", err)
			}
		}
	}()
	tools := conversationTools{
		analysis: toolGroups.Analysis,
		direct:   toolGroups.Direct,
		qa:       toolGroups.QA,
		report:   toolGroups.Report,
	}
	// 前端可见工具集
	visibleToolSet := buildVisibleToolSet(ctx, []string{}, toolGroups.Direct, toolGroups.QA, toolGroups.Analysis, toolGroups.Report)
	// chatModel
	chatModel, err := buildChatModel(ctx, a.GetConfig())
	if err != nil {
		logger.Error("buildChatModel err", err)
		return serviceDeps{}, err
	}
	// TODO chatModel 中间件
	var agentHandlers []adk.ChatModelAgentMiddleware
	// 数据层
	repo := buildRepo(a)
	// TODO RAG 工具
	// 主agent + adk Runner
	composeRunner, err := buildComposeRunner(
		chatModel,
		tools,
		repo,
		conf,
		agentHandlers,
		nil,
	)
	if err != nil {
		_ = err.Error()
		logger.Error("buildComposeRunner err:", err)
		return serviceDeps{}, err
	}
	// 返回依赖
	return serviceDeps{
		tools:         tools,
		toolProvider:  toolProvider,
		repo:          repo,
		composeRunner: composeRunner,
		visibleTools:  visibleToolSet,
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

func buildVisibleToolSet(
	ctx context.Context,
	toolName []string,
	toolLists ...[]tool.BaseTool,
) map[string]bool {
	visibleToolSet := make(map[string]bool)
	for _, name := range toolName {
		visibleToolSet[name] = true
	}
	for _, toolList := range toolLists {
		for _, item := range toolList {
			if item == nil {
				continue
			}
			info, err := item.Info(ctx)
			if err != nil || info == nil || info.Name == "" {
				continue
			}
			visibleToolSet[info.Name] = true
		}
	}
	return visibleToolSet
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
		WithMaxIterations(buildIterationLimits(conf.Agents.MaxIterations)).
		WithAgentHandler(agentHandler).
		WithRagTool(ragTool).Build()

	if err != nil {
		_ = err.Error()
		logger.Error("build compose runner error:", err)
		return nil, fmt.Errorf("build compose runner error: %w", err)
	}

	return composeRunner, nil
}

func buildIterationLimits(conf config.AgentMaxIterations) runner.IterationLimits {
	return runner.IterationLimits{
		Compose:              conf.Compose,
		ProjectQA:            conf.ProjectQA,
		RepoAnalyzer:         conf.RepoAnalyzer,
		RepoAnalyzerSubAgent: conf.RepoAnalyzerSubAgent,
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
