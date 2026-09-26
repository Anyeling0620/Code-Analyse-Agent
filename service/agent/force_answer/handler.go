package force_answer

import (
	"context"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type Config struct {
	MaxIterations  int
	ActiveKey      string
	Instruction    string
	FallbackAnswer string
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

func (h *ForceAnswerHandler) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext) (
	context.Context,
	*adk.ChatModelAgentState,
	error) {
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
	// 到达迭代次数 注入提示词 关闭本地工具列表 关闭服务商提供的工具列表
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

type modelWrapper struct {
	inner  model.BaseModel[*schema.Message]
	config Config
}

// WrapModel 最后一步如果还有工具调用，丢弃工具调用并返回fallbackAnswer
func (h *ForceAnswerHandler) WrapModel(ctx context.Context, m model.BaseModel[*schema.Message], _ *adk.ModelContext) (model.BaseModel[*schema.Message], error) {
	return &modelWrapper{inner: m, config: h.config}, nil
}

func (m *modelWrapper) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	msg, err := m.inner.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return replaceForcedToolCall(ctx, m.config, msg)
}
func (m *modelWrapper) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	stream, err := m.inner.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderWithConvert(stream, func(msg *schema.Message) (*schema.Message, error) {
		return replaceForcedToolCall(ctx, m.config, msg)
	}), nil
}

func replaceForcedToolCall(ctx context.Context, conf Config, msg *schema.Message) (*schema.Message, error) {
	active, err := getRunLocalValue(ctx, conf.ActiveKey)
	if err != nil {
		return nil, err
	}
	if active && msg != nil && len(msg.ToolCalls) > 0 {
		return schema.AssistantMessage(conf.FallbackAnswer, nil), nil
	}
	return msg, nil
}

func getRunLocalValue(ctx context.Context, key string) (bool, error) {
	value, found, err := adk.GetRunLocalValue(ctx, key)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	active, ok := value.(bool)
	return ok && active, nil
}
