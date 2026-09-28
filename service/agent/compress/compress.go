// Package compress 提供上下文压缩（context compaction）能力。
//
// 实现方式是复用 Eino 自带的 adk/middlewares/summarization 中间件，只做三件事：
//  1. 把配置翻译成触发策略（多少 token / 多少条消息之后开始压缩）；
//  2. 挂上观测回调，把"压缩前 / 压缩后"的消息条数写进日志；
//  3. 在未启用时返回 nil，让调用方完全不改变原有行为。
//
// 之所以不自己实现：token 统计、摘要生成、摘要替换、system 消息保留、用户消息回填
// 这些细节 Eino 中间件已经处理过，自行实现只会引入更多偏差。
package compress

import (
	"context"
	"fmt"

	"edu.agent.code/config"
	"edu.agent.code/utils/logger"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	// DefaultWindowTokens 是未配置时的模型上下文窗口（DeepSeek 常用 128k）。
	DefaultWindowTokens = 128000
	// DefaultTriggerRatio 是默认触发比例：窗口用到 60% 就开始压缩，
	// 而不是等到接近 100% 才截断——后者往往已经来不及，只能丢任务约束。
	DefaultTriggerRatio = 0.6
	// DefaultTriggerMessages 是消息条数的兜底阈值。
	DefaultTriggerMessages = 200
	// defaultInstruction 是内置的中文摘要指令，要求保留事实与证据位置。
	defaultInstruction = `请把上面的对话压缩成一份可继续执行任务的结构化摘要，必须保留：
1. 当前任务目标与用户最新要求；
2. 已确认的事实，并标注证据位置（文件路径、函数名、行号、命令与结果）；
3. 已做出的决策及理由；
4. 仍未解决的问题、缺失的证据和下一步；
5. 失败过的尝试及失败原因。
不要编造未确认的信息；不要把推断写成事实；不要把原始结论替换成模糊表述。`
)

// Policy 是压缩触发策略，导出以便单测与日志断言。
type Policy struct {
	// TriggerTokens 是触发压缩的 token 阈值（<=0 表示不按 token 触发）。
	TriggerTokens int
	// TriggerMessages 是触发压缩的消息条数阈值（<=0 表示不按条数触发）。
	TriggerMessages int
	// WindowTokens 是推导 TriggerTokens 时使用的窗口大小，仅用于观测。
	WindowTokens int
}

// Enabled 表示压缩是否启用。
func Enabled(conf config.ContextCompact) bool {
	return conf.Enabled
}

// BuildPolicy 把配置翻译成触发策略，并补齐默认值。
//
// 触发条件是"任一满足"：token 超过 TriggerTokens，或消息条数超过 TriggerMessages。
func BuildPolicy(conf config.ContextCompact) Policy {
	window := conf.WindowTokens
	if window <= 0 {
		window = DefaultWindowTokens
	}
	ratio := conf.TriggerRatio
	if ratio <= 0 || ratio > 1 {
		ratio = DefaultTriggerRatio
	}
	tokens := conf.TriggerTokens
	if tokens <= 0 {
		tokens = int(float64(window) * ratio)
	}
	messages := conf.TriggerMessages
	if messages <= 0 {
		messages = DefaultTriggerMessages
	}
	return Policy{
		TriggerTokens:   tokens,
		TriggerMessages: messages,
		WindowTokens:    window,
	}
}

// New 构造压缩中间件。未启用时返回 (nil, nil)，调用方直接跳过即可。
//
// chatModel 同时用于生成摘要；通常就是主链路的对话模型。
func New(ctx context.Context, conf config.ContextCompact, chatModel model.BaseModel[*schema.Message]) (adk.ChatModelAgentMiddleware, error) {
	if !conf.Enabled {
		return nil, nil
	}
	if chatModel == nil {
		return nil, fmt.Errorf("compress: chatModel is nil")
	}
	policy := BuildPolicy(conf)
	instruction := conf.Instruction
	if instruction == "" {
		instruction = defaultInstruction
	}
	middleware, err := summarization.New(ctx, &summarization.Config{
		Model:   chatModel,
		Trigger: &summarization.TriggerCondition{ContextTokens: policy.TriggerTokens, ContextMessages: policy.TriggerMessages},
		// 不开启内部事件：这些事件只允许在 adk Run/Resume 内发送，
		// 同时也避免新的事件类型涌进前端 SSE 流。压缩观测统一走 Callback + 日志。
		EmitInternalEvents: false,
		UserInstruction:    instruction,
		TranscriptFilePath: conf.TranscriptPath,
		Callback: func(ctx context.Context, before, after adk.ChatModelAgentState) error {
			// 压缩会影响后续所有轮次看到的历史，必须留下可观测痕迹：
			// before/after 的消息条数差异就是"这次压缩折叠掉了多少历史"。
			logger.Info("context compaction applied before_messages=%d after_messages=%d trigger_tokens=%d trigger_messages=%d window_tokens=%d",
				len(before.Messages), len(after.Messages), policy.TriggerTokens, policy.TriggerMessages, policy.WindowTokens)
			return nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("compress: build summarization middleware: %w", err)
	}
	logger.Info("context compaction enabled trigger_tokens=%d trigger_messages=%d window_tokens=%d",
		policy.TriggerTokens, policy.TriggerMessages, policy.WindowTokens)
	return middleware, nil
}

// NewHandlers 返回可以直接追加到 agent Handlers 的中间件列表。
// 未启用时返回 nil，保证调用方语义不变。
func NewHandlers(ctx context.Context, conf config.ContextCompact, chatModel model.BaseModel[*schema.Message]) ([]adk.ChatModelAgentMiddleware, error) {
	middleware, err := New(ctx, conf, chatModel)
	if err != nil {
		return nil, err
	}
	if middleware == nil {
		return nil, nil
	}
	return []adk.ChatModelAgentMiddleware{middleware}, nil
}
