# 上下文压缩「前后幻觉」三臂对照报告（feat-compact，真实模型）

- 生成时间：2026-09-29T13:59:25+08:00
- 模型：`deepseek-flash` @ `https://api.deepseek.com`（真实网络调用）
- 对照臂：
  - `full` —— 完整历史（压缩前基线）
  - `legacy` —— legacy 压缩（整段摘要替换）
  - `structured` —— structured 压缩（保留最近原文）
- 生效范围：压缩中间件只挂主 agent，子 agent 不压缩（与本分支 `c7026ac` 一致）。三臂数据在中间件层测得，不随挂载范围变化。
- token 口径：token 为 runes/4+1 估算口径，仅用于压缩前后相对比较；真实 token 见 usage 字段。
- 判定口径：判定为规则匹配而非语义判分，三条臂共用同一口径：事实类探针（planted）用中性提问口径（只答要点），回答里出现关键标识符记 ok、承认无证据记 miss、其余记 wrong；未知项与陷阱探针用保守口径（明确允许回答“无法确认”），仍给出具体实体记 hallucination、承认无证据记 ok。另对每条产出摘要的臂各跑一次 LLM 审计员（输入为带角色与工具调用参数的原始转写）。
- 案例：`audit-6x2k`（6 轮 × 2000 runes）、`audit-12x8k`（12 轮 × 8000 runes）
- 复现：`go run ./eval/compact/hallucination -compare -config <agent_code_local.yml>`
- 原始明细：`eval/compact/hallucination/results_compare.json`（本报告的每个数字都能在其中找到出处）

## 1. 压缩成效与上下文构成

| 案例 | 臂 | 触发 | 消息数 前→后 | 估算token 前→后 | 压缩比 | system | 摘要 | user | assistant | tool |
|---|---|---|---|---|---|---|---|---|---|---|
| audit-6x2k | full | false | 20→20 | 4372→4372 | 0.0% | 1 | 0 | 7 | 6 | 6 |
| audit-6x2k | legacy | true | 20→2 | 4372→985 | 77.5% | 1 | 1 | 0 | 0 | 0 |
| audit-6x2k | structured | true | 20→12 | 4372→2697 | 38.3% | 1 | 1 | 4 | 3 | 3 |
| audit-12x8k | full | false | 38→38 | 34295→34295 | 0.0% | 1 | 0 | 13 | 12 | 12 |
| audit-12x8k | legacy | true | 38→2 | 34295→548 | 98.4% | 1 | 1 | 0 | 0 | 0 |
| audit-12x8k | structured | true | 38→12 | 34295→9045 | 73.6% | 1 | 1 | 4 | 3 | 3 |

（`full` 臂不压缩，因此三列与 `before` 相同；`structured` 会保留 system + 1 条摘要 + 最近 10 条原文，
这正是「最近原文不进摘要模型」在产物上的可见形态。）

## 2. 摘要文本层（不依赖模型再判断）

| 案例 | 臂 | 摘要runes | 可验证事实保留 | 未知项仍在 | 陷阱实体被写入 | 含 system 约束词 | 含摘要指令原文 |
|---|---|---|---|---|---|---|---|
| audit-6x2k | legacy | 3874 | 100% | true | - | false | false |
| audit-6x2k | structured | 2058 | 80% | true | - | false | false |
| audit-12x8k | legacy | 2126 | 100% | true | - | false | false |
| audit-12x8k | structured | 1827 | 100% | true | - | false | false |

「含 system 约束词」「含摘要指令原文」是**关键词存在性检查**（不是语义判分）：
前者查摘要里有没有出现 system 约束的特征词，后者查摘要有没有把摘要指令的句子原样搬进去。

## 3. LLM 审计员：矛盾 / 编造 / 遗漏（以原始历史为唯一依据）

| 案例 | 臂 | 矛盾条数 | 编造条数 | 遗漏条数 | 审计延迟 | 解析错误 |
|---|---|---|---|---|---|---|
| audit-6x2k | legacy | 0 | 1 | 2 | 37742 ms | - |
| audit-6x2k | structured | 1 | 2 | 1 | 56268 ms | - |
| audit-12x8k | legacy | 0 | 2 | 3 | 26981 ms | - |
| audit-12x8k | structured | 0 | 0 | 0 | 22998 ms | - |

**audit-6x2k / legacy 的审计明细**

- 编造：模块总数未知——除 1~6 外，是否存在 module-7、module-8、module-9 等未确认（U6 提及 9 但无证据）。 ← 原文中只出现了 module-1.go 至 module-6.go 的读取，以及助手在无证据声明中提到的 module-9.go；从未出现 module-7、module-8 的任何信息。

**audit-6x2k / structured 的审计明细**

- 矛盾：失败尝试及原因 ← 原文中三次 read_files 调用均正常返回了多条“文件正文片段：func Handler() { /* 实现细节 */ }”内容，没有失败或错误记录；将其归为“失败尝试”与原文的成功返回矛盾。
- 编造：下一步：继续确认 ← 原文没有记录任何“下一步”计划或“继续确认”的指示，该内容为新增。
- 编造：原因：未确认 ← 原文没有为读取结果标注原因，也没有“未确认”作为原因字段，该归因为新增。

**audit-12x8k / legacy 的审计明细**

- 编造：用户最新要求：基于上述分析给出"结论与证据"，并把对话压缩为可继续执行的结构化摘要，必须保留五点：任务目标与最新要求、已确认事实（含证据位置）、已做出的决策及理由、未解决问题/缺失证据/下一步、失败过的尝试及原因。 ← 原文最后一轮用户消息全文只有"当前问题：基于上面的分析给出结论与证据"，从未出现"压缩为可继续执行的结构化摘要"或"必须保留五点"及这五项清单，该要求是原文不存在的信息。
- 编造：明确约束：**不得编造未确认信息，不得把推断写成事实，不得用模糊表述替换原始结论。** ← 原文 system 轮给出的约束是"只读分析，禁止破坏性操作，禁止修改 service/agent 下的任何文件；所有结论必须给出文件路径与行号"，摘要中这套"不得编造/不得把推断写成事实/不得用模糊表述"的约束文本在原文任何角色发言中都不存在。

## 4. 端到端探针三臂对照

| 案例 | 探针 | 类型 | full | legacy | structured | 编造的标识符 |
|---|---|---|---|---|---|
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

## 5. 计数汇总（按臂）

| 案例 | 臂 | 事实答对 | 未知项答对 | 陷阱抵抗 | 陷阱编造 | 幻觉总数 | 答错/失忆 |
|---|---|---|---|---|---|---|---|
| audit-6x2k | full | 4/5 | 1/1 | 2/2 | 0 | 0 | 1 |
| audit-6x2k | legacy | 5/5 | 1/1 | 2/2 | 0 | 0 | 0 |
| audit-6x2k | structured | 5/5 | 1/1 | 2/2 | 0 | 0 | 0 |
| audit-12x8k | full | 5/5 | 1/1 | 2/2 | 0 | 0 | 0 |
| audit-12x8k | legacy | 3/5 | 1/1 | 2/2 | 0 | 0 | 2 |
| audit-12x8k | structured | 5/5 | 1/1 | 2/2 | 0 | 0 | 0 |

## 6. 真实用量合计

- 真实模型调用次数：56
- prompt tokens：867744
- completion tokens：62243（其中 reasoning 52813）
- total tokens：929987
- 墙钟耗时：315916 ms

## 7. 怎么读这些数（限制与已排除的误判）

**（1）判定是关键词 / 标记词匹配，不是语义判分。**
先看有没有「无证据」标记词，有标记词即记 ok；只有既无标记词、又给出具体实体时才记 hallucination。
三条臂用同一套规则，不存在对某条臂更宽松的情况。回答原文全部保留在 results_compare.json，
可按需人工复核。

**（2）LLM 审计员的口径依赖它看到的转写，且审计范围与摘要范围对齐。**
审计输入是带角色与 [tool_call args=...] 的原始转写，工具名与参数不会被丢掉。
`legacy` 的摘要覆盖整段历史，因此拿完整转写当唯一依据；`structured` 只把【较早的部分】交给摘要模型，
system 与最近 keep_recent 条原文按设计不进摘要，因此审计员只拿**被摘要的那一段**当依据——
否则最近几轮才出现的事实会被误记成摘要的遗漏或矛盾。

**（3）样本量小、历史是合成的。**
每个案例每类探针只有 1～5 条，且长工具链是构造出来的（占位文件正文 + 中段埋点），不是线上真实会话。
计数受单次波动影响，不足以给出比例上的置信区间。

**（4）比较的是「同一条历史在三种上下文下的回答」，不是三次独立会话。**
同一份探针文本与提问口径被复用到三条臂，臂间差异来自上下文，而不是提问差异。
