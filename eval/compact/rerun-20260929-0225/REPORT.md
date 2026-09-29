# 上下文压缩真实模型评估（feat-compact）

> **中间产物，已被取代**：结论见最终版对比报告 [`eval/compact/REPORT.md`](../REPORT.md)。

- 生成时间：2026-09-29T02:25:08+08:00
- 摘要模型：`deepseek-flash` @ `https://api.deepseek.com`（真实网络调用）
- token 口径：token 为 runes/4+1 估算口径，仅用于压缩前后相对比较；真实 token 见 usage 字段。
- 复现：`go run ./eval/compact -config <agent_code_local.yml>`

配套表单：`eval/compact/context_compaction_real_model_eval.xlsx`（由 `eval/compact/build_xlsx.mjs` 从 `eval/compact/results.json` 生成）；原始明细见 `eval/compact/results.json`，问答原文见表单「回答原文」页。

## 1. 压缩成效（真实模型摘要）

| 案例 | 轮次×单条runes | 触发阈值 | 消息数 | 估算token 前→后 | 压缩比 | 真实prompt tokens | 真实completion | 真实延迟 |
|---|---|---|---|---|---|---|---|---|
| scale-6x2k | 6×2000 | 500 | 20→2 | 4352→726 | 83.3% | 7140 | 2249 | 10453 ms |
| scale-6x8k | 6×8000 | 500 | 20→2 | 17172→678 | 96.1% | 26544 | 1818 | 8948 ms |
| scale-12x8k | 12×8000 | 500 | 38→2 | 34275→536 | 98.4% | 52812 | 1498 | 7765 ms |

注：`真实prompt tokens` 是这次摘要调用真实上传的 prompt token（含 system + 全部历史 + 摘要指令），与左侧估算口径不同；中文场景下估算（runes/4）明显低于真实分词。

## 2. 关键事实保留率（摘要文本）

| 案例 | 保留率 | 未保留的事实 |
|---|---|---|
| scale-6x2k | 100% | - |
| scale-6x8k | 100% | - |
| scale-12x8k | 100% | - |

## 3. 触发阈值行为

| 案例 | 阈值 | 是否触发 | 摘要调用 | 估算token 前→后 |
|---|---|---|---|---|
| threshold-trigger-2000 | 2000 | true | 1 | 28573→645 |
| threshold-skip-100000 | 100000 | false | 0 | 28573→28573 |

## 4. 真实延迟分布（同形状重复 3 次）

| 运行 | 摘要调用延迟 | prompt tokens | completion tokens | reasoning tokens |
|---|---|---|---|---|
| latency-6x8k-run1 | 7118 ms | 26544 | 1456 | 492 |
| latency-6x8k-run2 | 8822 ms | 26544 | 1938 | 832 |
| latency-6x8k-run3 | 9076 ms | 26544 | 2055 | 851 |

## 5. 端到端问答保真（真实模型）

案例 `scale-12x8k`：对照臂 = 完整历史，实验臂 = 压缩后上下文。

| 事实 | 类型 | 关键词 | 完整历史答对 | 压缩后答对 |
|---|---|---|---|---|
| path | 证据位置 | `chunker.go` | true | true |
| symbol | 符号名 | `NewMilvus` | true | true |
| fuse | 决策 | `RRF` | true | true |
| collection | 标识符 | `p1c6a6a726a49c9a8` | true | true |
| dim | 数值 | `2048` | true | true |

**完整历史 5/5 正确，压缩后 5/5 正确。**
该阶段真实调用 11 次（含 1 次压缩），prompt 322661 tokens，completion 7236 tokens，耗时 43443 ms。

## 6. 真实用量合计

- 真实模型调用次数：18
- prompt tokens：532845
- completion tokens：20162（其中 reasoning 9392）
- total tokens：553007
- 墙钟耗时：104700 ms
