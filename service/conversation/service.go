package conversation

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/approval"
	"edu.agent.code/adaptor/repo/profile"
	"edu.agent.code/adaptor/repo/session"
	"edu.agent.code/config"
	"edu.agent.code/service/cost"
	"edu.agent.code/service/rag"
	"github.com/cloudwego/eino/adk"
)

type Service struct {
	adaptor adaptor.IAdaptor
	session session.ISession

	modelName string
	conf      *config.Config

	composeRunner *adk.Runner
	visibleTools  map[string]bool
	// TODO tools provider
	profiles  profile.IProfile
	approvals approval.Approval

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
		conf:          nil,
		composeRunner: nil,
		visibleTools:  nil,
		profiles:      nil,
		approvals:     approval.Approval{},
		cost:          deps.cost,
		rag:           nil,
	}, nil
}
