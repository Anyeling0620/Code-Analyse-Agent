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

	cost *cost.Service
	rag  *rag.Service
}

func NewService(ctx context.Context, adaptor adaptor.IAdaptor) (*Service, error) {
	conf := adaptor.GetConfig()
	deps, err := buildServiceDeps(ctx, adaptor)
	if err != nil {
		return nil, err
	}
	return &Service{
		modelName:     conf.DeepSeek.Model,
		conf:          conf,
		toolProvider:  deps.toolProvider,
		composeRunner: deps.composeRunner,
		visibleTools:  deps.visibleTools,
		profiles:      profile.NewProfile(adaptor),
		sessions:      session.NewSession(adaptor),
		approvals:     approval.NewApproval(adaptor),
		cost:          deps.cost,
		rag:           nil,
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
	err := s.cost.Track(ctx, runState.UserID, runState.SessionID, s.modelName, agentName, prompt, completion)
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
