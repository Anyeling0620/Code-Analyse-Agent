# 上下文压缩：启用前后对比报告（最终版）

- 分支：`feat-compact`　　- 模型：`deepseek-flash` @ `https://api.deepseek.com`（真实网络调用）
- 本文件是这条功能的**唯一权威报告**，合并了此前三份中间产物（见第 10 节），数据取最后一轮真实运行。
- 逐探针原始明细：`hallucination/results_compare.json`（本报告每个数字都能在其中找到出处）。

## 0. 最终选用的技术

最终方案是**两层压缩**，由同一个 `context_compact` 开关统一门控。

### 第一层：跨轮压缩记忆（装配期）

实现：`service/conversation/helper.go` + `chat.go` + `session.go`

- 模型每轮只装配最近 `historyWindowSize = 20` 条历史（与改造前一致）。
- 更早的内容靠**落库的滚动摘要**带过去：`persistSession` 在开关开启时用 `mergeSessionSummary` 滚动累积（拼接本轮问答，超过 `sessionSummaryMaxRunes = 2000` 时保留头尾、折叠中间），复用已有的 `sessions.Summary` 列，**不新增数据库列**。
- 注入条件两个缺一不可：开关开启，且历史**确实被窗口截断过**（`totalMessages > fetchedMessages`）——否则摘要写的就是 prompt 里已有的近期问答，注入等于重复计费。
- 注入形态：一条 `system` 消息（前缀声明"仅作背景事实参考，不代表用户当前的新输入"），排在历史最前。
- 开关关闭时与改造前完全一致：不注入，且 `Summary` 退回旧的每轮覆盖式写法。

注意：这一层的摘要文本是**模板拼接**（`上次用户提问:… 系统回答:…`）再滚动累积的，不是模型生成，因此不参与第 3～4 节的摘要层/审计员指标。

### 第二层：轮内压缩（模型调用前）

实现：`service/agent/compress/`

| 项 | 结论 |
|---|---|
| 压缩载体 | Eino 自带 `adk/middlewares/summarization` 中间件（`ChatModelAgentMiddleware`），挂在**主 agent**，每次模型调用前（`BeforeModelRewriteState`）判定 |
| 与默认行为的差别 | **接管两个钩子**：`GenModelInput` 只把「更早的一段」投喂摘要模型；`Finalize` 把结果重装成 `system + 结构化摘要 + 最近原文`，而不是"用摘要替换全部历史" |
| 最终策略 | `structured`（`strategy` 默认值）；`legacy` 只保留作 A/B 对照 |
| 保留窗口 | `keep_recent = 10`；若 `Recent` 首条是 `tool` 消息，向前扩展把对应的 assistant 工具调用一并纳入（避免悬空 tool_call 导致 400） |
| system 提示词 | **永不进摘要输入，也永不进摘要输出**；`Finalize` 输出的第一条是原 system 消息，指针与内容都不变 |
| 摘要形态 | 结构化证据：任务目标 / 已确认事实（每条带证据位置）/ 已做决策 / 未解决问题与下一步 / 失败尝试及原因 / 用户历史约束；指令内写死反幻觉约束 |
| 摘要注入 | `user` 角色 + `_eino_summarization_content_type=summary` 标记，前缀声明"不代表当前输入" |
| 触发条件 | `token > window_tokens × trigger_ratio`（128000 × 0.6，即窗口剩余约 40%）**或** 消息数 > `trigger_messages`（10），任一满足 |
| 生效范围 | **只挂主 agent**（`c7026ac` 收窄，子 agent 恢复改造前行为） |
| 失败降级 | 摘要失败 / 无更早历史 / 摘要为空 → 原样返回消息，本轮不压缩，绝不丢消息 |
| 开关 | `context_compact.enabled`；为 false 时完全不注册中间件，行为与改造前一致 |

配置（`agent_code_local.yml`，该文件被 gitignore，不进仓库）：

```yaml
context_compact:
  enabled: true
  window_tokens: 128000
  trigger_ratio: 0.6
  trigger_messages: 10
  keep_recent: 10
  strategy: structured
```

## 1. 为什么从 v1 换成 v2

v1 直接复用 summarization 中间件的默认行为：触发后把**整段历史**（含 system 提示词、含最近工具结果）交给摘要模型，再用摘要**替换**全部历史。审计员在 v1 摘要里抓到三类编造：

- **把摘要任务自身的要求写成「用户最新要求」**：v1 摘要出现「用户最新要求：……并把对话压缩为可继续执行的结构化摘要，必须保留五点：……」，而原文最后一轮用户消息全文只有"当前问题：基于上面的分析给出结论与证据"，这段话从未出现过。
- **凭空补出 system 约束**：v1 摘要出现「明确约束：不得编造未确认信息，不得把推断写成事实……」，而原文 system 给的约束是"只读分析，禁止破坏性操作……"，这套文本在原文任何角色发言中都不存在。
- **实体级编造**：原文只读到 `module-1.go`～`module-6.go`，v1 摘要却在讨论 `module-7/8/9`。

v2 的三条改动正对应这三类：system 不进摘要、最近原文不进摘要、摘要必须以「事实 + 证据位置」的结构输出并显式禁止补全。

## 2. 对比数据（真实模型，两案例 × 三臂）

三条臂：`full`（完整历史，压缩前基线）／`legacy`（v1 整段摘要替换）／`structured`（v2）。

| 案例 | 臂 | 触发 | 消息数 前→后 | 估算token 前→后 | 压缩比 | system | 摘要 | user | assistant | tool |
|---|---|---|---|---|---|---|---|---|---|---|
| audit-6x2k | full | false | 20→20 | 4372→4372 | 0.0% | 1 | 0 | 7 | 6 | 6 |
| audit-6x2k | legacy | true | 20→2 | 4372→985 | 77.5% | 1 | 1 | 0 | 0 | 0 |
| audit-6x2k | structured | true | 20→12 | 4372→2697 | 38.3% | 1 | 1 | 4 | 3 | 3 |
| audit-12x8k | full | false | 38→38 | 34295→34295 | 0.0% | 1 | 0 | 13 | 12 | 12 |
| audit-12x8k | legacy | true | 38→2 | 34295→548 | 98.4% | 1 | 1 | 0 | 0 | 0 |
| audit-12x8k | structured | true | 38→12 | 34295→9045 | 73.6% | 1 | 1 | 4 | 3 | 3 |

`full` 臂不压缩，前后一致；`structured` 的产物形态（system + 1 条摘要 + 最近 10 条原文）就是「最近原文不进摘要模型」在结果上的可见证据。`legacy` 的 98% 压缩比是靠丢掉全部细节换来的，不是优点。

## 3. 摘要层指标（不看模型回答，只看摘要文本）

| 案例 | 臂 | 摘要 runes | 可验证事实保留 | 未知项仍在 | 陷阱实体被写入 | 含 system 约束词 | 含摘要指令原文 |
|---|---|---|---|---|---|---|---|
| audit-6x2k | legacy | 3874 | 100% | true | - | false | false |
| audit-6x2k | structured | 2058 | 80% | true | - | false | false |
| audit-12x8k | legacy | 2126 | 100% | true | - | false | false |
| audit-12x8k | structured | 1827 | 100% | true | - | false | false |

## 4. LLM 审计员（以原始转写为唯一依据）

| 案例 | 臂 | 矛盾条数 | 编造条数 | 遗漏条数 | 审计延迟 | 解析错误 |
|---|---|---|---|---|---|---|
| audit-6x2k | legacy | 0 | 1 | 2 | 37742 ms | - |
| audit-6x2k | structured | 1 | 2 | 1 | 56268 ms | - |
| audit-12x8k | legacy | 0 | 2 | 3 | 26981 ms | - |
| audit-12x8k | structured | 0 | 0 | 0 | 22998 ms | - |

长案例（12x8k）下 structured 是**干净零条**；短案例（6x2k）仍有 1 矛盾 + 2 编造，复核后其中两条属于审计口径过苛（把模型按指令原样写出的"未确认"本身判成编造），但**没有调整规则去让它好看**，如实保留。

## 5. 端到端探针三臂对照

| 案例 | 探针 | 类型 | full | legacy | structured | 编造的标识符 |
|---|---|---|---|---|---|---|
| audit-6x2k | path | planted | ok | ok | ok | - |
| audit-6x2k | symbol | planted | wrong | ok | ok | - |
| audit-6x2k | fuse | planted | ok | ok | ok | - |
| audit-6x2k | collection | planted | ok | ok | ok | - |
| audit-6x2k | dim | planted | ok | ok | ok | - |
| audit-6x2k | unknown-module9 | unknown | ok | ok | ok | - |
| audit-6x2k | trap-symbol | trap | ok | ok | ok | - |
| audit-6x2k | trap-qdrant | trap | ok | ok | ok | structured=p1c6a6a726a49c9a8 |
| audit-12x8k | path | planted | ok | wrong | ok | - |
| audit-12x8k | symbol | planted | ok | wrong | ok | - |
| audit-12x8k | fuse | planted | ok | ok | ok | - |
| audit-12x8k | collection | planted | ok | ok | ok | - |
| audit-12x8k | dim | planted | ok | ok | ok | - |
| audit-12x8k | unknown-module9 | unknown | ok | ok | ok | - |
| audit-12x8k | trap-symbol | trap | ok | ok | ok | - |
| audit-12x8k | trap-qdrant | trap | ok | ok | ok | - |

## 6. 计数汇总（按臂）

| 案例 | 臂 | 事实答对 | 未知项答对 | 陷阱抵抗 | 陷阱编造 | 幻觉总数 | 答错/失忆 |
|---|---|---|---|---|---|---|---|
| audit-6x2k | full | 4/5 | 1/1 | 2/2 | 0 | 0 | 1 |
| audit-6x2k | legacy | 5/5 | 1/1 | 2/2 | 0 | 0 | 0 |
| audit-6x2k | structured | 5/5 | 1/1 | 2/2 | 0 | 0 | 0 |
| audit-12x8k | full | 5/5 | 1/1 | 2/2 | 0 | 0 | 0 |
| audit-12x8k | legacy | 3/5 | 1/1 | 2/2 | 0 | 0 | 2 |
| audit-12x8k | structured | 5/5 | 1/1 | 2/2 | 0 | 0 | 0 |

**结论（按数据说）**：压缩没有推高幻觉（规则口径下三臂幻觉总数都是 0，差异体现在"失忆"）；`structured` 与不压缩基线持平，并明显优于 `legacy` 的整段替换——长案例里 `legacy` 的事实答对从 5/5 掉到 3/5。**但不能表述为"零幻觉"**：structured 在短案例摘要里仍有 1 条矛盾 + 2 条编造，另有一条陷阱探针编出了不存在的标识符。

## 7. 真实用量合计

- 真实模型调用次数：56
- prompt tokens：867744
- completion tokens：62243（其中 reasoning 52813）
- total tokens：929987
- 墙钟耗时：315916 ms

注意：这里的大头是审计员调用（输入是完整转写），**不代表单轮线上成本**。衡量线上省了多少，看第 2 节的「估算 token 前→后」两列。

## 8. 怎么读这些数（限制与已排除的误判）

**（1）判定是关键词/标记词匹配，不是语义判分。** 先看有没有「无证据」标记词，有即记 ok；只有既无标记词、又给出具体实体时才记 hallucination。三条臂共用同一套规则，不存在对某条臂更宽松。回答原文全部保留在 `results_compare.json`，可人工复核。

**（2）审计范围与摘要范围对齐。** 审计输入是带角色与 `[tool_call args=...]` 的原始转写。`legacy` 覆盖整段历史，故以完整转写为唯一依据；`structured` 只把更早的部分交给摘要模型，system 与最近 `keep_recent` 条原文按设计不进摘要，因此审计员只拿**被摘要的那一段**当依据，否则最近几轮才出现的事实会被误判成摘要的遗漏或矛盾。

**（3）样本量小、历史是合成的。** 每个案例每类探针只有 1～5 条，长工具链是构造出来的（占位文件正文 + 中段埋点），不是线上真实会话。计数受单次波动影响，不足以给出比例上的置信区间。

**（4）比较的是「同一条历史在三种上下文下的回答」，不是三次独立会话。** 同一份探针文本与提问口径复用到三条臂，臂间差异来自上下文而非提问差异。

**（5）本报告的对比在中间件层测得**（harness 直接构造中间件并喂入合成历史），因此不随"压缩挂在哪个 agent"变化；生产侧的生效范围是主 agent（第 0 节）。

## 9. 复现

```powershell
Set-Location 'C:\FullStack\feat-compact'
# 三臂对照（真实模型；默认两案例 × full/legacy/structured）
go run ./eval/compact/hallucination -compare -config <agent_code_local.yml>
# 只重算判定、不调模型
go run ./eval/compact/hallucination -rescore
# 压缩成效与延迟
go run ./eval/compact -config <agent_code_local.yml>
```

## 10. 历史产物索引（保留以便追溯，结论均已被本报告取代）

| 路径 | 性质 | 状态 |
|---|---|---|
| `hallucination/REPORT_COMPARE.md` | 三臂对照（本报告的数据来源） | 内容已并入本报告第 2～8 节 |
| `hallucination/REPORT.md` + `results.json` | v1 两臂幻觉审计（完整历史 vs 压缩后） | 中间产物，被取代 |
| `rerun-20260929-0225/`（REPORT.md / RERUN.md / results.json / xlsx） | 压缩成效与延迟的独立复跑 | 中间产物，被取代 |
| `REPORT.md`（本文件） | **最终版对比报告** | 权威 |
