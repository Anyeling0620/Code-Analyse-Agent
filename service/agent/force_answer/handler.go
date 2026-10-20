package force_answer

import (
	"context"
	"edu.agent.code/utils/logger"

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

// BeforeModelRewriteState 在"最后一次还能调用模型"的轮次上摘掉工具并要求模型直接出结论。
//
// 触发条件是 remaining == 0，这个时机是可用的：eino 的 ChatModel 节点用 state pre-handler
// 做配额判断（adk/react.go 中 `if st.getRemainingIterations() <= 0 { return ErrExceedMaxIterations }`
// 然后 decrement），而 pre-handler 先于节点自身的执行体运行；本钩子位于节点执行体
// （typedStateModelWrapper.Generate）内部，因此它看到的是"已经减一"的值。
// 即 remaining 从 1 减到 0 的那一轮，模型仍然会被真正调用，钩子也能改写这次调用的输入。
// 只有当 remaining 已经是 0 时再进入该节点，pre-handler 才会先报错——那一轮本来就不该发生。
//
// 语义说明（Degraded）：
// 本路径下模型正常返回了最终回答，run 不算失败；但它是在证据不完整时被迫收尾的。
// 会话层看不到中间件是否触发（run-local value 不跨越 agent 边界），因此这里只打一条
// 结构化日志作为可观测点，不臆造 Degraded——Degraded 仍只在错误兜底路径
// （conversation.handleMaxIterationsExceeded）里置位，那条路径才是"没拿到报告"。
func (h *ForceAnswerHandler) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (
	context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || h.config.MaxIterations <= 0 {
		return ctx, state, nil
	}
	remainingIterations, err := getRemainingIterations(ctx)
	if err != nil {
		return ctx, state, err
	}
	if remainingIterations > 0 {
		return ctx, state, nil
	}
	err = adk.SetRunLocalValue(ctx, h.config.ActiveKey, true)
	if err != nil {
		return ctx, state, err
	}

	// ActiveKey 每个 Agent 类型各不相同，正好用来看是哪一层先耗尽：
	// compose_force_answer_active / repo_analyzer_force_report_active / repo_sub_agent_force_report_active。
	logger.Info("force_answer triggered key=%s max_iterations=%d remaining=%d tool_infos=%d",
		h.config.ActiveKey, h.config.MaxIterations, remainingIterations, len(state.ToolInfos))

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
