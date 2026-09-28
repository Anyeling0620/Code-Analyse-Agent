# 上下文压缩（Context Compaction）

## 1. 背景

改造前的问题（代码事实，不是推测）：

- `service/conversation/chat.go` 构建历史时固定 `Limit: 20`，没有全局 token 预算。
- `service/conversation/helper.go` 的工具结果截断只作用于 SSE 展示，模型实际看到的 ToolMessage 并没有被同等压缩。
- `session.Summary` 每轮被覆盖写入，且从未被注入模型输入，因此不承担长期记忆职责。
- 各工具只有单次输出上限（read_files 5000 runes、项目检索约 8000 runes 等），多次工具调用叠加后仍可能把上下文窗口顶满。

## 2. 方案

复用 Eino 自带的 `github.com/cloudwego/eino/adk/middlewares/summarization`（Eino v0.9.21），不自行实现 token 统计、摘要生成与消息替换。项目侧只做三件事：

1. 策略翻译：把配置翻译成触发条件（token 阈值 / 消息条数阈值）。
2. 接线：把中间件挂到主 Agent 与所有子 Agent 的 `Handlers` 上。
3. 观测：压缩发生时打日志（压缩前后消息条数、触发阈值、窗口大小）。

中间件挂在 `BeforeModelRewriteState` 上，也就是每次调用模型之前都会评估一次，因此它覆盖的是整个 ReAct 循环，而不是某一轮对话的入口。

### 2.1 两种策略：`structured`（默认）与 `legacy`

压缩有两种策略，由 `context_compact.strategy` 选择（空字符串等价于 `structured`）：

| 策略 | 摘要模型的输入 | 压缩后的消息 | 摘要提示词 |
|---|---|---|---|
| `structured`（默认） | **只有更早的历史**（Older） | 原来的 system 消息 + 1 条结构化摘要 + **最近 `keep_recent` 条原文** | 结构化证据模板（见下） |
| `legacy` | 全部非 system 上下文（含最近的工具结果） | 原来的 system 消息 + 1 条摘要（摘要文本里的 `<all_user_messages>` 段被回填成用户消息） | 旧的中文清单式指令 |

`legacy` 只是为了能原样复现改造前的行为（回滚与 A/B 对照），新配置一律用默认的 `structured`。

`structured` 的三段切分由 `compress.Segment` 完成，它保证三条不变量（都有单测）：

1. **system 消息永不进入摘要模型输入**，压缩后逐字保留且仍排在最前；
2. 最近 `keep_recent` 条（默认 10）消息**原样保留**，不经过摘要模型——当前任务与最近的工具结果因此不会被二次加工；
3. 若切点落在一条 `tool` 结果上，会向前扩展到对应的 assistant 工具调用，避免留下会让下一次模型调用直接 400 的悬空 `ToolMessage`。

只有更早的历史会被折叠成摘要，也就是所谓"牺牲一点上下文换更少的幻觉"：被牺牲的是远期历史，
换来的是近期事实与系统约束都不再经过一次模型改写。

默认的结构化摘要指令要求逐条给出证据位置（文件路径 / 函数名 / 配置键 / 行号 / 命令与结果），
并显式禁止三类污染（见 `structuredDefaultInstruction`）：
不得补全或新增上文未出现的文件、路径、模块、函数、配置键、集合名、ID、数字与命令；
不得把摘要任务自身的要求写成"用户的要求"；不得把推断写成事实，无法确认的一律写"未确认"。

**已知的旧行为问题**（有实证，见 `eval/compact/hallucination/REPORT.md` 第 2 节）：旧指令把
"保留：任务目标、已确认事实……"这份清单交给摘要模型后，模型会把这份清单**当成对话里用户的
最新要求**写进摘要，还会把原文没出现过的模块编号补进去。结构化指令把"引用边界"写死，就是为了
压掉这类污染。

### 新增与修改文件

| 文件 | 作用 |
|---|---|
| `config/config.go` | 新增 `context_compact` 配置块 |
| `service/agent/compress/compress.go` | 触发策略、两种压缩策略、三段切分、中间件构造与观测日志 |
| `service/agent/compress/compress_test.go` | 触发策略、失败降级、端到端触发与成效量化（含 legacy 契约） |
| `service/agent/compress/structured_test.go` | 结构化策略契约：三段切分、摘要输入边界、重装顺序、降级 |
| `service/conversation/depends.go` | `buildAgentHandlers` 构建并下发中间件 |
| `service/agent/runner/runner.go` | 把中间件透传给三个子 Agent |
| `service/agent/project_qa/agent.go` 等三个子 Agent | 新增 `Options.Handlers`，排在强制回答中间件之前 |
| `service/conversation/chat.go` | 模型输入改为：摘要、历史、用户画像 |
| `service/conversation/helper.go` | 摘要消息构造、滚动摘要、`buildModelHistory` |
| `service/conversation/session.go` | 会话摘要改为滚动累积，不再每轮覆盖 |

### 关键设计取舍

**中间件排在 force_answer 之前。** 先压缩历史、再决定是否强制收束，否则“直接回答”的指令会先落进历史、随后被摘要吞掉。

**不开启 EmitInternalEvents。** 这些内部事件只允许在 adk 的 Run/Resume 上下文内发送（否则报 `TypedSendEvent failed`），同时会把新的事件类型灌进前端 SSE 流。观测统一走 Callback 加日志。

**未启用时返回 nil。** `Enabled=false` 时 `New` 返回 `(nil, nil)`，不注册任何中间件，行为与改造前完全一致。

**system 消息不会被压掉。** `structured` 策略把 system 消息整段摘出来原样保留，且不把它们送进摘要模型；
`legacy` 策略下 Eino 的默认实现也保留**开头的** system 消息。压缩的目标是压掉工具输出这类易腐内容，
而不是压掉任务目标与约束。注意"回填用户消息"只是 `legacy` 走的 Eino 默认 finalizer 行为，
`structured` 不依赖它——它直接把最近原文留在消息里。

## 3. 配置

```yaml
context_compact:
  enabled: true
  window_tokens: 128000     # 模型上下文窗口
  trigger_ratio: 0.6        # 窗口用到 60% 就压缩（默认值）
  trigger_tokens: 0         # >0 时直接覆盖 trigger_ratio 的推导结果
  trigger_messages: 200     # 消息条数兜底
  model: ""                 # 摘要模型，留空沿用 deepseek.model
  instruction: ""           # 摘要指令，留空使用内置中文指令
  transcript_path: ""       # 完整会话记录路径，会写进摘要提示模型可回读
```

触发条件是“任一满足”：token 超过 `trigger_tokens`，或消息条数超过 `trigger_messages`。

`structured` 策略下另有两个字段：

```yaml
  strategy: "structured"    # structured（默认，推荐）/ legacy（旧行为，仅用于回滚与 A/B 对照）
  keep_recent: 10           # 原样保留的最近消息条数（不含 system）；<=0 用默认 10
```

`strategy` 写其它值（包括写错）都会回落到 `structured`：宁可走新行为，也不因为配置写错一个字
而静默退回旧行为。要回到旧行为必须显式写 `"legacy"`。

### 3.1 启用步骤（重要：只合并代码不会改变任何行为）

配置块默认不存在，`Enabled` 的零值即 `false`。也就是说**合入本分支后，不做任何配置改动，线上行为与改造前一模一样**——这一点是刻意保证的，避免一次合并悄悄改变每一次模型调用的 prompt。

启用需要在服务端配置文件（本地为 `agent_code_local.yml`，该文件不入库）里显式加上上面的 `context_compact` 块，并把 `enabled` 设为 `true`，然后重启服务。启动日志里会出现：

```
context compaction enabled strategy=structured keep_recent=10 trigger_tokens=76800 trigger_messages=200 window_tokens=128000
```

如果只看到 `context compaction uses a dedicated summary model model=...`，说明摘要模型被单独配置了。

`enabled: false` 的语义是**严格等价于改造前**，三处副作用同时关闭：

1. 不注册压缩中间件（`compress.New` 返回 `nil`）；
2. 不把跨轮摘要注入模型输入（`resolveSessionSummaryMessage`）；
3. 会话摘要不滚动累积，仍按改造前的方式每轮覆盖（`persistSession`）。

### 3.2 摘要注入的第二个条件

即使开关打开，摘要也只在该会话的历史**确实被 20 条窗口截断**（库里消息数 > 本轮取到的条数）时才注入。否则摘要里写的正是 prompt 中已经存在的近期问答，注入等于把同一段内容重复计费。

注意这只消除了"短会话整段重复"这一种情况。会话变长后摘要尾部（最近若干轮问答）仍会与窗口内的消息重叠，因为滚动摘要是按"每轮追加"累积的，不知道窗口边界。彻底对齐需要给摘要记一个"已折叠到第几条消息"的游标，见第 5 节未做项 5。

### 3.3 摘要失败不会打断用户这一轮

摘要调用是额外的一次模型调用，429/超时/5xx 都可能发生。两层保护：

1. `Retry` 显式打开（Eino 在 `Retry == nil` 时**只尝试一次**，空结构体对应默认 3 次重试 + 指数退避）；
2. 中间件外面包一层 `safeMiddleware`：重试后仍失败时记一条 warn 日志，返回原始 state 继续跑，本轮不压缩，下一次模型调用会重新评估。

对应测试：`TestNewConfiguresRetryForSummaryCalls`（断言真正传给 Eino 的配置里 `Retry` 非 nil）、`TestSummaryFailureDegradesToUncompactedTurn`（摘要模型持续报错时 `BeforeModelRewriteState` 返回 nil error 且 state 未被改写）。

## 4. 验证证据

在 worktree `C:\FullStack\feat-compact`（分支 `feat-compact`）中执行：

```
go build ./...   -> exit 0
go vet ./...     -> exit 0
go test ./...    -> 全部 ok（含新增的 compress 与 conversation 用例）
```

### 端到端触发（不是只测函数）

`TestCompactionFiresBeforeModelCall` 把中间件挂到真实 adk Agent 上，用真实 `Runner.Run` 跑一轮，断言模型实际收到的消息已经被压缩过：

```
context compaction applied strategy=structured system=1 older=9 recent=10 keep_recent=10 before_messages=20 after_messages=12 trigger_tokens=76800 trigger_messages=4 window_tokens=128000
compaction effect: messages 20 -> 12, estimated tokens 12034 -> 6042 (49.8% smaller)
```

（这是 `structured` 策略下的真实输出：20 条历史被切成"1 条 system + 9 条更早 + 10 条最近"，
压缩后是 12 条 = system + 摘要 + 最近 10 条，估算 token 减半。`legacy` 策略会把 20 条折成 2 条，
压缩比更高，代价是最近原文也被摘要改写——两者取舍见 `eval/compact/hallucination/REPORT_COMPARE.md`。）

### 配对安全

`TestLegacySummarizeFoldsHistoryAndKeepsPinnedContext` 断言 `legacy` 策略压缩后不存在孤立的 tool 消息
（`assistant tool_call` 与 `tool` 结果必须成对出现，悬空的 ToolMessage 会让下一次模型调用直接 400），
并断言 system 约束与用户意图都还在。`structured` 策略的对应断言在
`TestStructuredSummaryInputExcludesSystemAndRecent`（最近原文不进摘要输入、system 不进摘要输入）与
`TestStructuredFinalizeRebuildsSystemSummaryRecent`（重装顺序与原文指针不变）。

## 5. 成效评估

**有效的一面**

在一个“6 轮工具调用、每轮 8000 runes 工具结果”的合成会话里（共 20 条消息、约 12k 估算 token）：

| 策略 | 压缩后 | 估算 token | 压缩比 |
|---|---|---|---|
| `legacy` | 2 条（system + 摘要） | ~119 | 99% |
| `structured`（默认） | 12 条（system + 摘要 + 最近 10 条原文） | ~6042 | 49.8% |

`structured` 的压缩比明显更低，这是刻意的：被牺牲的是远期历史，最近 10 条保持原文，
换来的是近期事实与系统约束不再经过摘要模型改写。这类长工具链正是改造前最容易顶满窗口的场景。

触发点在线性增长的历史上会反复生效：只要跨过阈值就折叠一次，不依赖用户主动开新会话。

**成本与边界（必须说清楚）**

压缩不是免费的。每次触发都要额外调用一次模型生成摘要，摘要消息本身还有一个固定开销
（`legacy` 下是 Eino 的前导说明与继续指令，约 500 runes；`structured` 下是本包的前导说明，约 60 runes）。
因此当历史比摘要还小的时候触发压缩，反而会让 prompt 变大。这正是阈值必须存在、且不应设得过低的原因；
按默认的 60% 窗口（76.8k）触发时，这点开销可以忽略。

`structured` 另有一条兜底：若"更早的历史"为空（非 system 消息不超过 `keep_recent`），或摘要模型
返回空内容，`Finalize` 会**原样返回原消息**，等于本轮不压缩。代价是仍然白跑了一次摘要调用，
好处是绝不会因为压缩而丢消息（对应测试 `TestStructuredFinalizeFallsBackWhenNothingToCompress`、
`TestStructuredFinalizeFallsBackOnEmptySummary`）。

摘要是模型生成的，会丢信息。缓解手段是内置中文指令要求标注证据位置、区分已确认与未确认，同时 system 消息与用户消息不受影响。但它仍然是有损压缩，原始会话仍应保留在数据库中以便回读。

量化口径是估算。`estimated tokens` 按约 4 runes/token 估算，与 Eino 默认计数器在缺少 usage 时的兜底口径一致，只用于比较压缩前后，不代表真实分词结果。上线后应以 `service/conversation/service.go` 已记录的 prompt、completion、cache hit usage 做真实观测。

**本次未做（明确的后续项）**

1. 把 Eino 生成的摘要回写会话库。目前跨轮记忆走的是 `session.Summary`（滚动问答摘要），而 Eino 摘要在单次运行内有效。回写需要在 `compress` 暴露 hook，由 `conversation` 在 hook 里写回 session，并解决回写摘要与滚动摘要的覆盖顺序问题。
2. 按 token 用量触发强制收束。现有 `force_answer` 只看剩余迭代次数，还没有接入 token 使用率，窗口接近打满时“立即给出基于已确认事实的答案”尚未实现。
3. 压缩事件上报前端。目前只有日志，没有 SSE 事件，用户看不到上下文被压缩过。
4. `persistSession` 对 `session.Summary` 是"读出旧值 → 合并 → 写回"的读-改-写（既有模式，不是本次引入）。同一会话的两个请求并发落库时可能丢一次累加；本次只是让摘要变更更频繁，并未为此引入行锁或版本号。单账号单会话并发对话的场景下才可能出现。
5. 滚动摘要与历史窗口边界没有对齐（见 3.2）：摘要累积了每一轮问答，而窗口始终保留最近 20 条，两者在窗口附近重复。要做到不重复，需要把"已折叠到第几条消息"作为游标存进会话，并只把窗口之外的轮次写进摘要——这会改变 `session.Summary` 的语义与落库字段，本次没有做。
