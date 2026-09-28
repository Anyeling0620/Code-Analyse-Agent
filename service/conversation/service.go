package conversation

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/approval"
	"edu.agent.code/adaptor/repo/profile"
	"edu.agent.code/adaptor/repo/session"
	"edu.agent.code/config"
	"edu.agent.code/service/cost"
	"edu.agent.code/service/dto"
	"edu.agent.code/service/rag"
	"edu.agent.code/service/tool/provider"
	"edu.agent.code/utils/logger"
	"errors"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

type Service struct {
	//adaptor adaptor.IAdaptor
	//session session.ISession

	modelName string
	conf      *config.Config

	composeRunner *adk.Runner
	visibleTools  map[string]bool
	// PPT 中只有 sessions 没有 adaptor session
	sessions session.ISession
	// TODO tools provider
	toolProvider provider.IProvider

	profiles  profile.IProfile
	approvals approval.IApproval
	// shares 负责会话只读分享：快照落库、按令牌读取、撤销。
	shares session.IShare

	cost *cost.Service
	rag  *rag.Service

	// projectIndexer 负责项目级代码语义索引：按 project_id 隔离集合，
	// 供 repo_fetch 完成后异步建索引，并作为 rag_retriever 工具的检索后端。
	projectIndexer *rag.ProjectIndexer
}

func NewService(ctx context.Context, adaptor adaptor.IAdaptor, projectIndexer *rag.ProjectIndexer) (*Service, error) {
	conf := adaptor.GetConfig()
	deps, err := buildServiceDeps(ctx, adaptor, projectIndexer)
	if err != nil {
		return nil, err
	}
	return &Service{
		modelName:      conf.DeepSeek.Model,
		conf:           conf,
		toolProvider:   deps.toolProvider,
		composeRunner:  deps.composeRunner,
		visibleTools:   deps.visibleTools,
		profiles:       profile.NewProfile(adaptor),
		sessions:       session.NewSession(adaptor),
		approvals:      approval.NewApproval(adaptor),
		shares:         session.NewShare(adaptor),
		cost:           deps.cost,
		rag:            nil,
		projectIndexer: projectIndexer,
	}, nil
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	// 目前只关了 tool
	var err error
	if closeErr := s.toolProvider.Close(); closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	return err
}

func (s *Service) trackUsage(
	ctx context.Context,
	runState *dto.ChatRunState,
	msg *schema.Message,
	agentName string) error {
	if s.modelName == "" || msg == nil || runState == nil || msg.ResponseMeta == nil || msg.ResponseMeta.Usage == nil {
		return nil
	}
	usage := msg.ResponseMeta.Usage
	prompt, completion := int64(usage.PromptTokens), int64(usage.CompletionTokens)
	cached := int64(usage.PromptTokenDetails.CachedTokens) // 命中 prompt cache 的输入 token
	err := s.cost.Track(ctx, runState.UserID, runState.SessionID, s.modelName, agentName, prompt, cached, completion)
	if err != nil {
		logger.Error("cost.Track err",
			zap.Any("modelName", s.modelName),
			zap.Any("agentName", agentName),
			zap.Any("runState", runState),
			zap.Any("msg", msg),
			zap.Error(err))
		return err
	}
	return nil
}
