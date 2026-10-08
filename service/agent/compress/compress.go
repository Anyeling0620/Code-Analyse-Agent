// Package compress 提供上下文压缩（context compaction）能力。
//
// 实现方式是复用 Eino 自带的 adk/middlewares/summarization 中间件，负责：
//  1. 把配置翻译成触发策略（多少 token / 多少条消息之后开始压缩）；
//  2. 挂上观测回调，把"压缩前 / 压缩后"的消息条数与三段构成写进日志；
//  3. 在未启用时返回 nil，让调用方完全不改变原有行为。
//
// 压缩策略有两种，由 ContextCompact.Strategy 选择：
//
//   - structured（默认）：把消息切成三段 —— system 原样保留且永不进摘要输入、
//     最近 KeepRecent 条原样保留、只有更早的历史进摘要模型；摘要按固定小节输出
//     结构化证据（事实必须带证据位置，无证据的写"未确认"）。摘要文本只做补充，
//     不替换近期原文，也不替换系统提示词。
//   - legacy：改造前的行为 —— 整段历史（含 system）交给摘要模型，再用摘要替换全部历史。
//     保留它用于回滚与 A/B 对照。
//
// token 统计、重试、摘要消息的角色与标记等细节仍交给 Eino 处理，只有"摘要模型的输入"
// 与"压缩后的消息怎么重装"两处由本包接管（GenModelInput / Finalize）。
package compress

import (
	"context"
	"fmt"
	"strings"

	"edu.agent.code/config"
	"edu.agent.code/service/dto"
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
	// DefaultKeepRecent 是结构化压缩时原样保留的最近消息条数。
	//
	// 只保留"更早的历史"需要摘要：最近这些条通常正是当前任务的上下文，
	// 一旦过一遍摘要模型就可能被改写，而用户明确要求"牺牲一点上下文换更少的幻觉"。
	DefaultKeepRecent = 10
	// StrategyStructured 是默认策略：system 永不进摘要 + 保留最近原文 + 结构化证据摘要。
	StrategyStructured = "structured"
	// StrategyLegacy 是改造前的策略：整段历史摘要后替换。
	//
	// 保留它有两个用处：一是让 A/B 评测能原样复现旧行为，二是线上可以通过配置回滚。
	StrategyLegacy = "legacy"
	// legacyDefaultInstruction 是旧策略的内置摘要指令，要求保留事实与证据位置。
	legacyDefaultInstruction = `请把上面的对话压缩成一份可继续执行任务的结构化摘要，必须保留：
1. 当前任务目标与用户最新要求；
2. 已确认的事实，并标注证据位置（文件路径、函数名、行号、命令与结果）；
3. 已做出的决策及理由；
4. 仍未解决的问题、缺失的证据和下一步；
5. 失败过的尝试及失败原因。
不要编造未确认的信息；不要把推断写成事实；不要把原始结论替换成模糊表述。`

	// structuredDefaultInstruction 是结构化策略的内置摘要指令。
	//
	// 与旧指令的关键差别：旧指令里"保留什么"的清单本身会被摘要模型当成对话内容，
	// 出现过把"压缩成结构化摘要"写成"用户最新要求"的实证（见 eval/compact/hallucination/REPORT.md）。
	// 所以这里显式划定边界：只能引用上文、不得补全、摘要任务自身的要求不算用户要求。
	structuredDefaultInstruction = `把上面对话中【较早的部分】压缩成一份结构化证据摘要，供后续继续任务时参考。

只允许引用上面对话里已经出现过的内容。必须遵守：
1. 不得补全或新增上文未出现的文件、路径、模块、函数、配置键、集合名、ID、数字与命令；
2. 不得把本摘要任务的要求（包括本提示里的任何句子）写成"用户的要求"；
3. 不得把推断写成事实。无法从上文确认的，一律写"未确认"；
4. 标识符（文件路径、函数名、配置键、集合名、ID、数值）必须原样抄写，不得改写或翻译。

严格按下面六个小节的标题输出，没有内容的写"无"：

【任务目标】
【已确认事实】每条写成：事实 —— 证据位置（文件路径/函数名/配置键/行号/命令与结果）；无证据的写"未确认"
【已做决策】
【未解决问题与下一步】
【失败尝试及原因】
【用户历史约束】逐字引用上文用户提出的约束，不得改写`
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
//
// 调用方必须用它来门控所有压缩相关的副作用（注册中间件、注入跨轮摘要、
// 累积会话摘要），否则会出现"配置写 enabled: false，跨轮摘要却照样进 prompt"
// 这种开关说了不算的情况。
func Enabled(conf config.ContextCompact) bool {
	return conf.Enabled
}

// newSummarization 是 summarization.New 的间接引用：单测替换它就能拿到真正传给
// Eino 的配置（例如确认 Retry 非 nil），不必靠"故意失败几次再看耗时"这种间接证据。
var newSummarization = summarization.New

// safeMiddleware 把"压缩失败"降级为"本轮不压缩"。
//
// 压缩只是优化：摘要模型 429/超时/5xx、或摘要结果不合规时，summarization
// 中间件会把错误直接从 BeforeModelRewriteState 返回，整轮对话就此中断——
// 而用户要的回答并不依赖这次压缩成功与否。这里吞掉错误、保留原始 state，
// 让主流程带着未压缩的历史继续跑；下一次模型调用还会重新评估是否压缩。
//
// 只覆写 BeforeModelRewriteState：summarization 中间件也只实现这一个钩子，
// 其余钩子沿用 BaseChatModelAgentMiddleware 的空实现，语义不变。
type safeMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	inner adk.ChatModelAgentMiddleware
}

func (m *safeMiddleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState,
	mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	nextCtx, nextState, err := m.inner.BeforeModelRewriteState(ctx, state, mc)
	if err != nil {
		logger.Warn("context compaction skipped: summarization failed, continue with uncompacted history err=%v messages=%d",
			err, len(state.Messages))
		return ctx, state, nil
	}
	if nextState == nil {
		// 中间件不该返回 nil state；真出现时按"未压缩"处理，避免把 state 丢掉。
		return ctx, state, nil
	}
	return nextCtx, nextState, nil
}

// ResolveStrategy 把配置里的策略解析成明确取值。
//
// 空字符串与其它非法取值都回落到 StrategyStructured：默认走更保守的新行为，
// 只有显式写 "legacy" 才回到旧行为，避免"配置写错一个字就静默退回旧策略"。
func ResolveStrategy(conf config.ContextCompact) string {
	if strings.TrimSpace(conf.Strategy) == StrategyLegacy {
		return StrategyLegacy
	}
	return StrategyStructured
}

// ResolveKeepRecent 解析"原样保留的最近消息条数"，<=0 时使用 DefaultKeepRecent。
func ResolveKeepRecent(conf config.ContextCompact) int {
	if conf.KeepRecent <= 0 {
		return DefaultKeepRecent
	}
	return conf.KeepRecent
}

// Segmented 是压缩前的消息三段切分。
type Segmented struct {
	// System 是 system 消息：原样保留，且永远不进入摘要模型输入。
	System []*schema.Message
	// Older 是更早的非 system 消息：唯一会被折叠成摘要的一段。
	Older []*schema.Message
	// Recent 是最近 KeepRecent 条非 system 消息：原样保留。
	Recent []*schema.Message
}

// Segment 按 keepRecent 把消息切成三段；keepRecent<=0 时使用 DefaultKeepRecent。
//
// 三条不变量（都有单测）：
//  1. system 消息全部进 System，既不参与 Recent 的计数，也不会出现在 Older 里；
//  2. 只有"最后 keepRecent 条非 system 消息"之外的更早消息才会进 Older；
//  3. 若 Recent 的第一条是 tool 结果，会向前扩展到对应的 assistant 工具调用，
//     避免留下"配不上 tool_call 的悬空 ToolMessage"（那会让下一次模型调用直接 400）。
func Segment(messages []*schema.Message, keepRecent int) Segmented {
	if keepRecent <= 0 {
		keepRecent = DefaultKeepRecent
	}
	var segmented Segmented
	rest := make([]*schema.Message, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		if message.Role == schema.System {
			segmented.System = append(segmented.System, message)
			continue
		}
		rest = append(rest, message)
	}
	if len(rest) <= keepRecent {
		segmented.Recent = rest
		return segmented
	}
	cut := len(rest) - keepRecent
	for cut > 0 && rest[cut].Role == schema.Tool {
		cut--
	}
	segmented.Older = rest[:cut]
	segmented.Recent = rest[cut:]
	return segmented
}

// summaryExtraKey / summaryExtraValue 与 Eino 内部标记"这条 user 消息是摘要"的
// extra 字段保持一致：这样后续轮次再做压缩时，Eino 的默认处理也不会把摘要本身
// 当成用户的新输入。
const (
	summaryExtraKey   = "_eino_summarization_content_type"
	summaryExtraValue = "summary"
)

// summaryPreamble 说明这段结构化证据的来源与可信边界。
const summaryPreamble = "以下是本次会话较早轮次的压缩摘要（结构化证据），只包含上文出现过的事实；" +
	"未确认的一律标注为未确认，不代表用户当前的新输入："

// buildGenModelInput 构造结构化策略下"摘要模型实际看到什么"。
//
// 关键点：只投喂 Older 一段。业务 system 消息（安全约束、画像）与最近原文
// 都不进摘要输入，所以摘要模型既无法改写系统规则，也无法二次加工近期事实。
func buildGenModelInput(keepRecent int) summarization.GenModelInputFunc {
	return func(_ context.Context, sysInstruction, userInstruction *schema.Message,
		originalMsgs []*schema.Message) ([]*schema.Message, error) {
		segmented := Segment(originalMsgs, keepRecent)
		input := make([]*schema.Message, 0, len(segmented.Older)+2)
		input = append(input, sysInstruction)
		input = append(input, segmented.Older...)
		input = append(input, userInstruction)
		return input, nil
	}
}

// buildFinalize 重装压缩后的历史：原 system 消息 + 结构化摘要 + 最近原文。
//
// 与旧行为最大的差别是"不再用摘要替换全部历史"：最近 keepRecent 条保持逐字不变，
// 所以当前任务与最近工具结果不会经过摘要模型这一层。
func buildFinalize(keepRecent int, transcriptPath string) summarization.FinalizeFunc {
	return func(_ context.Context, originalMessages []*schema.Message,
		summary *schema.Message) ([]*schema.Message, error) {
		segmented := Segment(originalMessages, keepRecent)
		// 没有更早的历史可压缩，或摘要模型没给出内容 —— 放弃本次压缩。
		// 这里是"宁可少压缩，也绝不丢消息"的兜底：原样返回，等价于这轮不压缩。
		if len(segmented.Older) == 0 || summary == nil {
			return originalMessages, nil
		}
		content := strings.TrimSpace(summary.Content)
		if content == "" {
			return originalMessages, nil
		}
		if transcriptPath != "" {
			content += fmt.Sprintf("\n\n（完整原始会话记录：%s；需要原文时回读核对，不要凭记忆补全）", transcriptPath)
		}
		// 摘要用 user 角色：与 Eino 默认实现一致（newTypedSummaryMessage 也是 user），
		// 并且带上同样的 content_type 标记，便于后续轮次识别。
		summaryMessage := schema.UserMessage(summaryPreamble + "\n" + content)
		if summaryMessage.Extra == nil {
			summaryMessage.Extra = map[string]any{}
		}
		summaryMessage.Extra[summaryExtraKey] = summaryExtraValue

		rebuilt := make([]*schema.Message, 0, len(segmented.System)+1+len(segmented.Recent))
		rebuilt = append(rebuilt, segmented.System...)
		rebuilt = append(rebuilt, summaryMessage)
		rebuilt = append(rebuilt, segmented.Recent...)
		return rebuilt, nil
	}
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
// 返回的中间件带一层降级保护：摘要失败不会打断用户这一轮（详见 safeMiddleware）。
func New(ctx context.Context, conf config.ContextCompact, chatModel model.BaseModel[*schema.Message]) (adk.ChatModelAgentMiddleware, error) {
	if !conf.Enabled {
		return nil, nil
	}
	if chatModel == nil {
		return nil, fmt.Errorf("compress: chatModel is nil")
	}
	policy := BuildPolicy(conf)
	strategy := ResolveStrategy(conf)
	keepRecent := ResolveKeepRecent(conf)
	instruction := conf.Instruction
	if instruction == "" {
		if strategy == StrategyLegacy {
			instruction = legacyDefaultInstruction
		} else {
			instruction = structuredDefaultInstruction
		}
	}
	summarizationConfig := &summarization.Config{
		Model:   chatModel,
		Trigger: &summarization.TriggerCondition{ContextTokens: policy.TriggerTokens, ContextMessages: policy.TriggerMessages},
		// 显式打开重试：Eino 在 Retry 为 nil 时只尝试一次
		// （summarization.generateWithRetry：retryCfg == nil 直接单次调用），
		// 而摘要失败在老的写法下会直接终止用户这一轮。空结构体即默认值，
		// 对应 MaxRetries=3、标准退避。
		Retry: &summarization.RetryConfig{},
		// 不开启内部事件：这些事件只允许在 adk Run/Resume 内发送，
		// 同时也避免新的事件类型涌进前端 SSE 流。压缩观测统一走 Callback + 日志。
		EmitInternalEvents: false,
		UserInstruction:    instruction,
		// TranscriptFilePath 只在未设置 Finalize 时由 Eino 自动追加；
		// 结构化策略下由 buildFinalize 自己拼，所以这里只在旧策略传值。
		TranscriptFilePath: conf.TranscriptPath,
		Callback: func(ctx context.Context, before, after adk.ChatModelAgentState) error {
			// 压缩会影响后续所有轮次看到的历史，必须留下可观测痕迹：
			// before/after 的消息条数差异就是"这次折叠掉了多少历史"。
			// 三段条数只在结构化策略下有意义（旧策略不做保留），所以分开记，避免误导。
			//
			// 同时给 run 级埋点计数：压缩次数是判断 context_compact.trigger_ratio
			// 是否合理的唯一数据来源。Callback 只在压缩**真正应用**后才触发，
			// 摘要失败走 safeMiddleware 的降级路径不会到这里，所以计数不会虚高。
			dto.RunCountersFromContext(ctx).AddCompaction()
			detail := "strategy=" + StrategyLegacy
			if strategy == StrategyStructured {
				segmented := Segment(before.Messages, keepRecent)
				detail = fmt.Sprintf("strategy=%s system=%d older=%d recent=%d keep_recent=%d",
					StrategyStructured, len(segmented.System), len(segmented.Older),
					len(segmented.Recent), keepRecent)
			}
			logger.Info("context compaction applied %s before_messages=%d after_messages=%d trigger_tokens=%d trigger_messages=%d window_tokens=%d",
				detail, len(before.Messages), len(after.Messages),
				policy.TriggerTokens, policy.TriggerMessages, policy.WindowTokens)
			return nil
		},
	}
	if strategy == StrategyStructured {
		summarizationConfig.GenModelInput = buildGenModelInput(keepRecent)
		summarizationConfig.Finalize = buildFinalize(keepRecent, conf.TranscriptPath)
	}
	middleware, err := newSummarization(ctx, summarizationConfig)
	if err != nil {
		return nil, fmt.Errorf("compress: build summarization middleware: %w", err)
	}
	logger.Info("context compaction enabled strategy=%s keep_recent=%d trigger_tokens=%d trigger_messages=%d window_tokens=%d",
		strategy, keepRecent, policy.TriggerTokens, policy.TriggerMessages, policy.WindowTokens)
	return &safeMiddleware{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}, inner: middleware}, nil
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
