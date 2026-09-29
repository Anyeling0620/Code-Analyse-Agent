package conversation

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/approval"
	"edu.agent.code/adaptor/repo/checkpoint"
	"edu.agent.code/adaptor/repo/profile"
	"edu.agent.code/adaptor/repo/session"
	"edu.agent.code/config"
	"edu.agent.code/service/agent/compress"
	"edu.agent.code/service/agent/runner"
	"edu.agent.code/service/agent/skill"
	"edu.agent.code/service/cost"
	"edu.agent.code/service/rag"
	"edu.agent.code/service/tool/provider"
	"edu.agent.code/service/tool/repo_fetch"
	"edu.agent.code/service/tool/terminal"
	"edu.agent.code/utils/dsml"
	"edu.agent.code/utils/logger"
	"fmt"
	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"path/filepath"
	"strings"
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
}

func buildServiceDeps(ctx context.Context, a adaptor.IAdaptor, projectIndexer *rag.ProjectIndexer) (deps serviceDeps, err error) {
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
			// 注意：这里不能写 err = toolProvider.Close()。
			// 那样会在 Close 成功（返回 nil）时把真正的初始化错误覆盖成 nil，
			// 导致 NewService 返回一个 composeRunner 为 nil 的"半成品"服务，
			// 直到第一次对话才以 nil panic 的形式暴露出来。
			if closeErr := toolProvider.Close(); closeErr != nil {
				logger.Error("closing tool provider err", closeErr)
			}
		}
	}()
	tools := conversationTools{
		analysis: toolGroups.Analysis,
		direct:   toolGroups.Direct,
		qa:       toolGroups.QA,
		report:   toolGroups.Report,
	}
	// repo_fetch：把 git URL / 本地路径统一解析成工作区内稳定的项目根目录。
	// 它同时承担"仓库就绪后触发异步建索引 + 回写会话项目上下文"的职责。
	repoFetchTool, err := repo_fetch.NewTool(repo_fetch.WithHooks(buildRepoFetchHooks(projectIndexer)))
	if err != nil {
		logger.Error("buildServiceDeps repo_fetch.NewTool err", err)
		return serviceDeps{}, err
	}
	tools.direct = append(tools.direct, repoFetchTool)

	// 项目级 RAG 检索工具：按当前会话的项目隔离集合，替换原先的全局检索工具。
	var ragTool tool.BaseTool
	if projectIndexer != nil {
		ragTool, err = projectIndexer.Tool(ctx)
		if err != nil {
			logger.Error("buildServiceDeps projectIndexer.Tool err", err)
			return serviceDeps{}, err
		}
	}
	if ragTool == nil {
		// 项目索引不可用（例如 RAG 被关闭）时退回原有全局检索工具，
		// 保证工具集里不会出现 nil，行为与改造前保持一致。
		ragTool, err = toolProvider.RetrieverTool(ctx)
		if err != nil {
			logger.Error("buildServiceDeps RetrieverTool err", err)
			return serviceDeps{}, err
		}
	}
	if ragTool != nil {
		tools.analysis = append(tools.analysis, ragTool)
		tools.qa = append(tools.qa, ragTool)
	}
	// 前端可见工具集
	visibleToolSet := buildVisibleToolSet(ctx, []string{repo_fetch.ToolName}, tools.direct, tools.qa, tools.analysis, toolGroups.Report)
	// chatModel
	chatModel, err := buildChatModel(ctx, a.GetConfig())
	if err != nil {
		logger.Error("buildChatModel err", err)
		return serviceDeps{}, err
	}
	// Skill 中间件
	agentHandlers, skillToolNames, err := buildAgentHandlers(ctx, conf, chatModel)
	if err != nil {
		logger.Error("buildAgentHandlers err", err)
		return serviceDeps{}, err
	}
	lo.ForEach(skillToolNames, func(item string, index int) {
		visibleToolSet[item] = true
	})
	// 数据层
	repo := buildRepo(a)
	// 主agent + adk Runner
	composeRunner, err := buildComposeRunner(
		chatModel,
		tools,
		repo,
		conf,
		agentHandlers,
		ragTool,
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
		WithToolMiddleware([]compose.ToolMiddleware{
			terminal.NewApprovalMiddleware(repo.approvals),
		}).
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

// buildRepoFetchHooks 构造 repo_fetch 成功后的联动逻辑：
//  1. 回写本轮会话上下文，保证同一轮结束时持久化的会话已经带上项目根目录，
//     这样第二轮追问"刚才那个项目"时 project_qa / rag_retriever 才知道分析的是哪个项目；
//  2. 异步触发项目级语义索引，不阻塞 repo_fetch 返回，也不占用分析主流程。
func buildRepoFetchHooks(projectIndexer *rag.ProjectIndexer) repo_fetch.Hooks {
	return repo_fetch.Hooks{
		OnReady: func(ctx context.Context, r repo_fetch.Result) {
			if session := turnSessionFromContext(ctx); session != nil && r.Root != "" {
				session.CurrentProjectRoot = r.Root
				session.CurrentProjectName = filepath.Base(r.Root)
			}
			if projectIndexer == nil || r.Root == "" {
				return
			}
			// HTTP 请求结束后 ctx 会被取消，这里必须脱离取消信号，让索引在后台跑完。
			indexCtx := context.WithoutCancel(ctx)
			ref := rag.ProjectRef{ProjectID: r.ProjectID, Root: r.Root, Commit: r.Commit}
			go func() {
				if err := projectIndexer.EnsureIndexed(indexCtx, ref); err != nil {
					logger.Error("project rag EnsureIndexed failed",
						zap.String("project_id", ref.ProjectID),
						zap.String("root", ref.Root),
						zap.Error(err))
					return
				}
				logger.Info("project rag index ready project_id=%s", ref.ProjectID)
			}()
		},
	}
}

func buildChatModel(ctx context.Context, conf *config.Config) (model.ToolCallingChatModel, error) {
	return buildChatModelWithName(ctx, conf, conf.DeepSeek.Model)
}

// buildChatModelWithName 用同一个 DeepSeek 端点构造指定名称的对话模型。
func buildChatModelWithName(ctx context.Context, conf *config.Config, modelName string) (model.ToolCallingChatModel, error) {
	baseModel, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		APIKey:  conf.DeepSeek.APIKey,
		Timeout: time.Minute * 5,
		BaseURL: conf.DeepSeek.BaseURL,
		Model:   modelName,
	})
	if err != nil {
		logger.Error("buildChatModel NewChatModel err:%v model=%s", err, modelName)
		return nil, err
	}
	return dsml.WrapEinoModel(baseModel), nil
}

// buildSummarizeModel 返回生成摘要用的模型。
//
// 默认沿用主链路的对话模型；当 context_compact.model 显式配置了另一个模型时，
// 单独构造一个，避免"配置了 model 却静默不生效"。
func buildSummarizeModel(ctx context.Context, conf *config.Config,
	fallback model.BaseModel[*schema.Message]) (model.BaseModel[*schema.Message], error) {
	name := strings.TrimSpace(conf.ContextCompact.Model)
	if name == "" || name == conf.DeepSeek.Model {
		return fallback, nil
	}
	summarizeModel, err := buildChatModelWithName(ctx, conf, name)
	if err != nil {
		return nil, fmt.Errorf("build summarization model %q: %w", name, err)
	}
	logger.Info("context compaction uses a dedicated summary model model=%s", name)
	return summarizeModel, nil
}

func buildAgentHandlers(ctx context.Context, conf *config.Config,
	chatModel model.BaseModel[*schema.Message]) ([]adk.ChatModelAgentMiddleware, []string, error) {
	handlers := make([]adk.ChatModelAgentMiddleware, 0, 2)
	skillToolNames := make([]string, 0, 1)
	skillHandler, skillTooName, err := skill.BuildMiddleware(ctx, conf.Skills)
	if err != nil {
		logger.Error("buildAgentHandlers BuildMiddleware error",
			zap.Any("conf", conf), zap.Error(err))
		return nil, nil, fmt.Errorf("buildAgentHandlers init skill middleware error: %w", err)
	}
	if skillHandler != nil {
		handlers = append(handlers, skillHandler)
		skillToolNames = append(skillToolNames, skillTooName)
	}
	// 上下文压缩中间件：未启用时返回 nil，保持原有行为。
	summarizeModel, err := buildSummarizeModel(ctx, conf, chatModel)
	if err != nil {
		logger.Error("buildAgentHandlers buildSummarizeModel error", zap.Error(err))
		return nil, nil, fmt.Errorf("buildAgentHandlers init compact model error: %w", err)
	}
	compactHandlers, err := compress.NewHandlers(ctx, conf.ContextCompact, summarizeModel)
	if err != nil {
		logger.Error("buildAgentHandlers compress error", zap.Error(err))
		return nil, nil, fmt.Errorf("buildAgentHandlers init compact middleware error: %w", err)
	}
	handlers = append(handlers, compactHandlers...)
	return handlers, skillToolNames, nil
}
