package force_answer

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type Config struct {
	MaxIterations int
	ActiveKey     string
	Instruction   string
}

type ForceAnswerHandler struct {
	*adk.BaseChatModelAgentMiddleware
	config Config
}

func NewForceAnswerHandler(config Config) adk.ChatModelAgentMiddleware {
	return &ForceAnswerHandler{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		config:                       config,
	}
}

func (h *ForceAnswerHandler) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (
	context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || h.config.MaxIterations <= 0 {
		return ctx, state, nil
	}
	remainingIterations, err := getRemainingIterations(ctx)
	if err != nil {
		return ctx, state, err
	}
	if remainingIterations != 0 {
		return ctx, state, nil
	}
	err = adk.SetRunLocalValue(ctx, h.config.ActiveKey, true)
	if err != nil {
		return ctx, state, err
	}

	state.Messages = append(state.Messages, schema.UserMessage(h.config.Instruction))
	state.ToolInfos = []*schema.ToolInfo{}
	state.DeferredToolInfos = nil
	return ctx, state, nil
}

func getRemainingIterations(ctx context.Context) (int, error) {
	remaining := -1
	err := compose.ProcessState(ctx, func(ctx context.Context, state *adk.State) error {
		remaining = state.RemainingIterations
		return nil
	})
	return remaining, err
}
