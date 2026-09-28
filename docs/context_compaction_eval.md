# 上下文压缩成效评估（独立复现）

本文档是**独立于实现者**的成效评估，只新增测试与文档，未修改任何实现代码。
被测对象：`feat-compact` 分支上的 `service/agent/compress`（封装 Eino v0.9.21
`adk/middlewares/summarization`）与 `service/conversation` 的跨轮滚动摘要。

## 1. 复现方式

```bash
cd <feat-compact worktree>
go test ./service/agent/compress/ ./service/conversation/ -run TestEval -v -count=1
```

评估代码：`service/agent/compress/compact_eval_test.go`、`service/conversation/summary_eval_test.go`。

口径与限制（先声明，避免误读）：

- token 用约 4 runes/token **估算**，与 Eino 在缺少真实 usage 时的兜底口径一致，只用于**前后对比**，不代表真实分词。
- 摘要模型是本地 stub，**不发起网络请求**；因此耗时只反映中间件自身开销，不含真实模型延迟。
- 压缩入口走真实中间件方法 `BeforeModelRewriteState`，触发判断逻辑本身被覆盖。

## 2. 压缩成效（单次 ReAct 循环内）

场景：多轮「读大文件」工具调用，最后一条是当前问题。

| 规模 | 消息数 | 估算 token | 压缩比 |
|---|---|---|---|
| 6 轮 × 2k runes | 20 → 2 | 4260 → 177 | **95.8%** |
| 6 轮 × 8k runes | 20 → 2 | 17081 → 177 | **99.0%** |
| 12 轮 × 8k runes | 38 → 2 | 34149 → 213 | **99.4%** |

结论：压缩把「随工具调用线性增长的历史」折叠成「system + 一条摘要」，长工具链不再线性推高 prompt。

## 3. 压缩后不变量（全部通过）

`TestEvalPostCompactionInvariants` 断言：

1. system 约束原样保留，且仍在最前；
2. 最后一条 user 消息（当前任务）仍在；
3. 摘要内容确实作为消息注入；
4. tool 配对安全：无孤立 tool 消息，也无「有 tool_call 无结果」的悬空调用；
5. 消息条数确实下降。

## 4. 成本与代价（必须知道的三个数）

**（1）每次触发多一次模型调用，且这次调用要重读全部历史。**
`TestEvalCompactionOverhead` 实测：摘要提示 27 条消息、约 **11427 估算 token**，
压缩后结果约 189 估算 token。也就是说，压缩「省下的是后续每一轮的 prompt」，
代价是「当次一次全量重读」。中间件自身耗时（stub）在 0～1.3ms 量级，可忽略；
真实成本几乎全部来自那次模型调用。

**（2）历史比摘要还短时，压缩是净亏。**
`TestEvalSummaryOverheadCrossover` 扫描结果：

| 单条工具结果 runes | 压缩前 token | 压缩后 token | 净变化 |
|---|---|---|---|
| 100 | 46 | 148 | **+102（变差）** |
| 200 | 83 | 148 | **+65（变差）** |
| 400 | 157 | 148 | -9（开始变好） |
| 800 | 296 | 148 | -148 |
| 4000 | 1434 | 148 | -1286 |

临界点约在 **400 runes/工具结果**；换句话讲，摘要消息自身固定开销约 **148 估算 token（≈600 runes）**。
这也是阈值不能设得过低的原因：默认 0.6 × 128k = 76.8k token 触发，远高于这个临界点，不存在净亏风险。

**（3）阈值行为符合预期。**
`TestEvalThresholdSensitivity` 在同一份 28459 估算 token 的历史上：
阈值 500 / 2000 / 5000 / 25000 都触发并压到约 201 token；阈值 100000 不触发、prompt 不变（28459）。
说明触发条件与「不触发时完全不改变行为」都成立。

## 5. 跨轮部分的成效

`TestEvalCrossTurnPromptIsCapped`（20 轮、每轮约 4000 runes 工具结果）：

| 输入形状 | 消息数 | 估算 token |
|---|---|---|
| 原始历史 | 61 | 31711 |
| 滚动摘要 + 最近 4 条 | 5 | 3424 |

**下降 89.2%**。`TestEvalRollingSummaryStaysBounded` 连续合并 40 轮后，摘要稳定在
2000 runes 上限（`sessionSummaryMaxRunes`），且最早任务目标与最新进展都还在
——即上限生效、头部目标与尾部进展被保留，中间被折叠。

## 6. 发现的隐含契约（值得记录）

用户意图回填依赖模型按提示输出 `<all_user_messages>...</all_user_messages>` 块：
Eino 的默认 finalizer 是用真实用户消息**替换**该块（`summarization.go:792`）。
如果摘要模型没有按提示输出这个块，用户消息就不会被回填。
`TestEvalFinalizerBackfillsUserIntent` 在「模型遵守提示」的假设下验证回填成立。
生产环境的缓解手段是 Eino 内置指令本身要求输出该块，且项目自定义指令也要求保留用户最新要求。

## 7. 结论

- 压缩在真实形状的长工具链上**有效且显著**：单次循环内 95%～99% 体积下降，跨轮 89% 下降。
- 压缩**安全**：system 约束、当前任务、tool 配对三类关键信息在实测中都保住了。
- 压缩**有代价且可界定**：一次额外全量重读（1.1k 量级 token 的历史 ≈ 11k 估算 token 的摘要提示），
  外加约 148 估算 token 的常驻摘要开销；只要触发阈值不低到与摘要同量级，就始终是净收益。
- 本次评估**未覆盖**：真实模型摘要质量（是否丢失或写错事实）、真实网络延迟与失败率、
  摘要回写会话库、以及按 token 用量触发的强制收束（force_answer 目前只看迭代次数）。
  这几项需要接入真实模型与 usage 数据后再测。
