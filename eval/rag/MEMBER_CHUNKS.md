# 大括号语言类成员抽取：改造前后对比

对应提交：`6a208e1 feat(rag): 大括号语言抽取类成员，方法级符号成块`

## 1. 改了什么

`splitBraceSymbols` 原来只用"花括号深度归零"取**顶层**块。JS/TS 的类里，
方法在相对深度 1，于是整个类是一块；超长后被行窗二次切成一堆
`symbol` 相同、内容重叠的碎片。后果是"某个方法实现在哪"这类问题
在候选池里根本找不到对应的块。

现在识别 `class / interface / struct / impl / object / record / enum / trait`
等容器，把内部方法抽成独立块：

- 方法块：`symbol = 容器.方法`、`kind = method`、`parent = 容器名`
- 头部块：容器声明到第一个方法之前（保留类注释与字段声明）
- 各块严格铺满容器区间，不丢内容也不重叠；字符串与注释里的花括号一律跳过
- 方法体内的嵌套闭包、类字段里的对象字面量都不会被误判成方法

## 2. 索引层证据（Milvus 实测）

同一仓库 `axios/axios`（commit `2426e03`），同一 collection，重建前后对比：

| kind | 改造前 | 改造后 | 变化 |
| --- | ---: | ---: | ---: |
| `method` | **0** | **137** | **+137** |
| `type` | 245 | 214 | −31 |
| `func` | 579 | 573 | −6 |
| `block` | 784 | 784 | 0 |
| `file_summary` | 219 | 219 | 0 |
| `text`（回退路径） | 1426 | 1426 | 0 |
| **合计** | **3253** | **3353** | **+100（+3.1%）** |

`method = 0 → 137` 是本次改动生效的直接证据。`type` 减少是因为容器被拆成
"头部块 + 方法块"，`func` 略减是少数对象字面量成员被正确归类为方法。

## 3. 单文件证据：`lib/core/Axios.js`

改造前（11 块）：**8 个 `type` 窗片** + `func generateHTTPMethod` + 文件摘要 + block。
8 个窗片的 symbol 全是 `Axios`，行区间互相重叠：16-56、50-84、75-110、105-146、
138-176、171-202、197-236、231-266。

改造后（14 块）：

| kind | symbol | lines |
| --- | --- | --- |
| type | `Axios` | 16-23 |
| method | `Axios.constructor` | 24-31 |
| method | `Axios.request` | 32-65 |
| method | `Axios.request` | 61-81 |
| method | `Axios._request` | 83-114 |
| method | `Axios._request` | 106-151 |
| method | `Axios._request` | 143-177 |
| method | `Axios._request` | 172-205 |
| method | `Axios._request` | 198-242 |
| method | `Axios._request` | 232-259 |
| method | `Axios.getUri` | 261-266 |
| block | — | 268-280 |
| func | `generateHTTPMethod` | 282-307 |

头部块不再吞掉方法体（`16-23` 而非 `16-56`），方法级 symbol 可被检索命中。
`_request` 仍被切成 6 段，因为单方法 176 行超过 1200 字符预算——这是二次切的
**设计内行为**，但每段都带 `parent = Axios._request`，语义上可归并。

> 关于 `parent` 的证据强度：切分器确实产出该字段（单测
> `TestSplitBraceSymbolsExtractsClassMembers` 断言 `Parent == "Client"`），
> `service/rag/service.go:314` 也会把它写进 chunk metadata；
> 但 `eval/rag` 的导出器不包含 `parent`，所以**本次未在 Milvus 侧确认该字段**，
> 只在切分器输出层确认过。

## 4. 检索层证据（axios 20 题，真实已合并 PR 出题）

同一套 `queryset.axios.json`，同一套 arm，只换索引：

| arm | | F@1 | F@5 | F@10 | F@20 | MRR |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| dense_zh | 改造前 | 0.20 | 0.45 | 0.60 | 0.70 | 0.325 |
| | 改造后 | **0.30** | **0.50** | **0.65** | **0.75** | **0.404** |
| dense_en | 改造前 | **0.35** | 0.55 | **0.70** | 0.75 | **0.425** |
| | 改造后 | 0.30 | **0.60** | 0.65 | 0.75 | 0.420 |
| sparse_zh | 改造前 | 0.00 | 0.00 | 0.00 | 0.00 | 0.001 |
| | 改造后 | 0.00 | 0.00 | 0.00 | 0.00 | 0.001 |
| hybrid_zh | 改造前 | **0.30** | 0.60 | 0.65 | 0.70 | **0.405** |
| | 改造后 | 0.20 | 0.60 | **0.70** | **0.75** | 0.357 |
| hybrid_en | 改造前 | 0.15 | 0.45 | 0.60 | 0.75 | 0.287 |
| | 改造后 | **0.20** | 0.45 | 0.60 | **0.80** | **0.336** |
| hybrid_zh_norerank | 改造前 | 0.00 | 0.15 | 0.45 | 0.60 | 0.116 |
| | 改造后 | 0.00 | 0.10 | 0.45 | **0.65** | 0.107 |
| hybrid_en_norerank | 改造前 | 0.05 | **0.40** | **0.60** | **0.80** | **0.206** |
| | 改造后 | 0.05 | 0.25 | 0.55 | 0.70 | 0.157 |

口径：`n = 20`，**1 道题 = 5pp**。噪声地板就是这么高。

## 5. 被证伪的假设：那三道 `Axios.js` 题

改造前预判"ground truth 指向 `lib/core/Axios.js` 的三道题会因此改善"。
实测（首次命中该文件的排名，`>20` 表示 20 名内没有）：

| 题号 | arm | 改造前 | 改造后 |
| --- | --- | ---: | ---: |
| a002 | dense_zh / dense_en / hybrid_zh / hybrid_en | >20 | >20 |
| a010 | dense_zh | 38 | >20 |
| a010 | dense_en | >20 | 32 |
| a017 | dense_zh | 13 | 11 |
| a017 | dense_en | 10 | 15 |
| a017 | hybrid_zh | 17 | 15 |
| a017 | hybrid_en | >20 | 20 |

**没有任何一道题进入 top-10，改善在噪声范围内。假设不成立。**

## 6. 结论

1. **结构目标达成**：`method` 级块 `0 → 137`，`Axios.js` 从"7 个同名窗片"
   变成"类头部 + 10 个具名方法"，元数据与行区间自洽。
2. **检索增益未被证实**：F@1 两升两降三平，MRR 两升三降一平；
   只有 F@20 是 4 升 1 降（净 +10pp），方向偏正但幅度在噪声内。
   可以说的最强表述是"深度召回略有改善，精度指标无变化"。
3. **"类体碎片化挡住了方法级检索"这个判断是错的**。真正的瓶颈是别的东西——
   本仓库实测 top-5 大量落在 `docs/`、`README`、`CHANGELOG` 这类散文上，
   而它们从不是"实现在哪"的答案。跨语言鸿沟与散文过采才是主因。

## 7. 顺带修掉的一个崩溃

新加的真实仓库回归测试（对 `workspace/repos/` 下全部 410 个源文件跑切分）
立刻抓到一个切片越界 panic：相邻顶层块重叠时，调用方会把块的起点往后夹，
夹过开括号后再用 `content[start:openIdx]` 切片就越界。
现在夹过开括号就退回原来的整块行为。

## 8. 口径与限制

- `n = 20` 的评测集，单题权重 5pp，**任何 ±5pp 的变化都不构成证据**。
- 只跑了一个陌生仓库（axios，JS 为主）。Java/Rust/C# 的容器抽取未经真实数据验证。
- 本题材下 `S@`（符号级召回）指标口径已废弃，报告一律不列。

## 9. 复现

```powershell
# 重建索引（会用新切分器）
go run ./tmp_unseen_index "C:\FullStack\Code Analyse Agent\workspace\repos\axios-axios"

# 查看索引构成
go run ./eval/rag -collection edu_agent_code_docs_p24e1f35abce5e675 -dump -dump-out "$env:TEMP\axios.json"

# 重跑评测
go run ./eval/rag -collection edu_agent_code_docs_p24e1f35abce5e675 `
  -queryset eval/rag/queryset.axios.json `
  -out eval/rag/results.axios.members.json -rewrite-arm=false
```
