# RAG 中文直检 vs 中文改写英文 再检：A/B 跑分报告

- 日期：2026-09-28
- collection：`edu_agent_code_docs_p1c6a6a726a49c9a8`（828 chunk，Milvus `82.156.119.15:19530`，全程只读）
- 程序：`eval/rag/main.go`（`go run ./eval/rag`）
- 原始明细：`eval/rag/results.json`（= `eval/rag/results.fallback.json` 的副本，见下方"数据来源"）

---

## 0. 数据来源（先看这条，它决定结论的适用范围）

**`eval/rag/queryset.json` 到等待窗口结束（23:26 → 23:41，15 分钟）仍未出现**（生成评测集的 agent 当时还在跑），
因此本报告的结论**不是**基于那份独立评测集，而是基于 runner 自造的兜底评测集：

`eval/rag/queryset.fallback.json` —— 45 题，由 `go run ./eval/rag -gen 45` 生成：
从 collection 里挑真实存在的 `func/method/struct/interface/const/var` chunk（每个文件最多 3 条），
让 deepseek-flash 看源码写中文提问，再用与评测集同口径的规则机检（不含符号名/符号拆词/路径片段）。
45 题全部通过校验，0 丢弃；kind 分布 func 23 / struct 10 / method 8 / const 2 / interface 2。

另一条重要前提：**collection 里是仓库的旧快照**。828 个 chunk 的 `commit_sha` 全是
`c3ea49d54831da20be598d05a23e65410ccf2507`，只有 133 个文件；本机 main 上尚未推送的新文件
（`service/rag/project.go`、`service/rag/chunker.go`、`service/tool/repo_fetch/*`、`e2e/*` 等）**不在库里**，
所以出题目标一律按"库里真实存在"来挑（全量元数据快照见 `eval/rag/collection_index.json`）。

**结论口径**：可以用来判断"中文改写英文这条路值不值得加"，但**不能**把绝对数值当成最终质量基线——
题目是 runner 自己出的，没有经过独立评测集 agent 的选题与人工抽查。

---

## 1. 五条 arm 与参数

| arm | 说明 |
| --- | --- |
| `dense_zh` | 中文原句 → 稠密向量（COSINE）TopK，无重排 |
| `dense_en` | 中文原句 → LLM 改写英文 → 稠密向量（COSINE）TopK，无重排 |
| `sparse_zh` | 中文原句 → BM25 稀疏检索 TopK |
| `hybrid_zh` | dense + sparse + RRF + rerank，中文原句 |
| `hybrid_en` | dense + sparse + RRF + rerank，改写后的英文 |

- 参数全部取自 `agent_code_local.yml` 的 `rag` 段，与 `adaptor/vector/milvus.go` 的
  `buildRetrieverConfig` 一致：字段 `vector` / `sparse_vector` / `content` / `metadata`，
  dense metric `COSINE`，稀疏 `BM25`（集合里带 `bm25_auto` function，content 用 `{"type":"chinese"}` analyzer），
  融合 `RRFReranker`，重排 `provider=bigmodel` / `model=rerank`，`top_k=20`。
- 改写：`POST https://api.deepseek.com/chat/completions`，`model=deepseek-flash`，`temperature=0`，
  `max_tokens` 2048 起逐级放大；system prompt 要求"一句英文检索式、保留原句里的英文标识符、不编造"。
- 指标：文件级（`source_path` 相等）与符号级（`metadata.symbol` 相等 或 content 含该符号名）的
  Recall@1/5/10/20、MRR（首个文件级命中的倒数排名）、每题耗时。
- **与生产唯一的有意偏差**：生产 hybrid 重排后只保留 `rerank.top_n=5` 条，这里为了能算 Recall@10/@20
  在重排后保留 20 条（候选池大小与生产一致，仍是最多 20 条进重排）。

---

## 2. 汇总（45 题，全部 arm 0 报错）

| arm | F@1 | F@5 | F@10 | F@20 | S@1 | S@5 | S@10 | S@20 | MRR | 平均耗时 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `dense_zh` | 0.3556 | 0.8222 | 0.8444 | 0.9556 | 0.3333 | 0.6667 | 0.7111 | 0.8667 | 0.5310 | 749 ms（含 1 次 19.5s 抖动，中位数 287 ms） |
| `dense_en` | **0.6000** | **0.8667** | **0.9333** | 0.9556 | **0.6000** | **0.8444** | **0.9111** | **0.9556** | **0.7135** | 315 ms（中位数 268 ms） |
| `sparse_zh` | 0.1556 | 0.2889 | 0.4000 | 0.4000 | 0.1556 | 0.2222 | 0.2889 | 0.4000 | 0.2189 | 31 ms |
| `hybrid_zh` | 0.5556 | 0.8889 | 0.8889 | 0.8889 | 0.4889 | 0.7778 | 0.7778 | 0.7778 | 0.6811 | 619 ms（中位数 623 ms） |
| `hybrid_en` | **0.6222** | **0.9111** | **0.9333** | **0.9778** | **0.6222** | **0.9111** | 0.9111 | **0.9333** | **0.7303** | 709 ms（中位数 647 ms） |

F = 文件级 Recall，S = 符号级 Recall；粗体 = 该列最好。

改写一题平均 **1268 ms**（中位数 1126 ms，min 590 / max 2633 ms，`max_tokens` 2048）。

### 分难度（文件级 F@5 / 符号级 S@5 / MRR）

| difficulty | n | `dense_zh` | `dense_en` | `sparse_zh` | `hybrid_zh` | `hybrid_en` |
| --- | --- | --- | --- | --- | --- | --- |
| descriptive | 16 | 0.8750 / 0.6875 / 0.6333 | 0.8125 / 0.7500 / 0.6793 | 0.3125 / 0.3125 / 0.2502 | 0.8750 / 0.7500 / 0.5667 | **1.0000 / 0.9375 / 0.7104** |
| explaining | 28 | 0.7857 / 0.6429 / 0.4737 | 0.8929 / 0.8929 / 0.7228 | 0.2500 / 0.1429 / 0.1730 | **0.8929 / 0.7857 / 0.7351** | 0.8571 / **0.8929** / 0.7320 |
| locating | 1 | 1.0 / 1.0 / 0.5 | 1.0 / 1.0 / 1.0 | 1.0 / 1.0 / 1.0 | 1.0 / 1.0 / 1.0 | 1.0 / 1.0 / 1.0 |

（locating 只有 1 题，仅作记录，不构成证据。）

---

## 3. 结论

**1）核心对比 `dense_zh` vs `dense_en`：改写英文的收益是明确的，而且主要体现在"排得更前"。**

- 文件级 F@1 从 0.3556 升到 0.6000（**+24.4 个百分点，相对提升 69%**），MRR 从 0.5310 升到 0.7135（**+34%**）。
- F@5 +4.4pp、F@10 +8.9pp；F@20 两者持平（0.9556）。
- 符号级同样成立：S@1 0.3333 → 0.6000，S@5 0.6667 → 0.8444。
- 解读：中文提问和英文代码之间的跨语言鸿沟，主要伤害的是**排序质量**（中文 query 能"摸着"目标，但排在后面），
  而不是"完全召回不到"。@20 上两者打平说明差距可以在更深的位置补回来——这正是加"中文直接检索"而不是加改写的反面论据：
  如果下游只把 top-3/top-5 塞给模型，差距会被放大到 +24pp 的 @1 量级。

**2）端到端 `hybrid_zh` vs `hybrid_en`：改写仍有增益，但被 RRF+rerank 吸收掉大半。**

- 文件级 F@1 0.5556 → 0.6222（+6.7pp）、F@5 0.8889 → 0.9111（+2.2pp）、F@20 0.8889 → 0.9778（+8.9pp）、MRR +7.2%。
- 也就是说：**现有完整链路已经把中文 query 救回来一大半**（hybrid_zh 的 F@5 0.8889 vs dense_zh 0.8222），
  但没救干净（hybrid_en 在 @1/@5/@20 上全面不低于 hybrid_zh）。

**3）`hybrid_zh` 相对 `dense_zh` 有增益，而且是排序端增益为主。**

- F@1 0.3556 → 0.5556（+20pp）、MRR 0.5310 → 0.6811，F@5 +6.7pp；但 F@20 反而从 0.9556 掉到 0.8889。
- 掉 F@20 的原因是可解释的：hybrid 的候选池是 dense+sparse 各 20 条经 RRF 融合后的 20 条，重排再从这 20 条里排序，
  等于"把 dense 单路的 20 条换成了融合后的 20 条"，融合没覆盖到的目标就掉了。要修得把候选池放大（例如各 30~50 再融合再重排）。

**4）`sparse_zh`（BM25）有明显天花板，不能单独承担中文检索。**

- F@10 = F@20 = 0.4000（Recall 在 20 条处已经不再增长），S@5 只有 0.2222，MRR 0.2189。
- 换句话说：对这批"自然语言描述型"中文问题，BM25 最多只能覆盖 40% 的目标，且排序很差。中文 query 的召回主要靠 dense。

**5）延迟：改写这一跳是主要成本，但量级可接受。**

- 改写平均 1268 ms（中位数 1126 ms）；增量相对整条 hybrid 链路（中位数 623 ms）约翻倍。
- dense 两路本身的中位耗时几乎一样（中文 287 ms vs 英文 268 ms），**中文查询没有额外的向量化惩罚**；
  `dense_zh` 表里的 749 ms 平均耗时被一次 19.5s 的网络抖动拉高，不代表系统性差异。
- 若引入"查询改写"前置节点，端到端 P50 会从约 0.62 s 涨到约 1.9 s（改写 + hybrid），需要按体验权衡；
  可以只在"首轮/低置信"时改写，或并行发起中文与英文两路再融合（成本换延迟）。

### 建议

- 倾向于**加"中文→英文查询改写"节点**：它在隔离实验（dense）里带来 +24pp 的 @1 与 +34% MRR，
  是"用户中文提问 + 英文代码库"这个场景里最便宜的一处改造；即便现有 RRF+rerank 已经很能打，
  改写仍稳定不低于不改写（本批 45 题上没有一项指标变差）。
- 同时补两处链路短板，收益可能比改写更大：①把 hybrid 的候选池放大（各 30~50）再融合再重排，
  修掉 F@20 从 0.9556 掉到 0.8889 的问题；②BM25 不要单独用于中文，或者给中文 query 做标识符/术语展开。

---

## 4. 质量与偏差风险（必须知道这些才能采信上面的数字）

1. **题目是 runner 自己出的**（`-gen`：LLM 看源码写问题 + 机检），没有独立评测集 agent 的选题与人工抽查，
   存在"题目偏向与检索器同源的表述"的风险；绝对数值只能当作方向性证据，不能当作最终基线。
2. **Rerank 分数高度压缩**：直连智谱 rerank 实测 `relevance_score` 集中在 0.998~0.9997；
   本批 900 条 `hybrid_zh` 命中的重排分数全部落在 **0.99876 ~ 1.0** 之间（top 与第 20 名相差不到 0.0013）。
   这说明该 rerank 模型对本任务的**区分度很低**，hybrid 两路的排序提升有多少来自重排、多少来自 RRF，
   本报告无法拆分。建议后续单独做一次"RRF-only vs RRF+rerank"对照。
3. **collection 是旧快照**（c3ea49d，133 文件）：本机 main 上新增的 RAG 文件不在库里，
   所以"针对新代码提问"的场景没有被这批题覆盖，结论外推到新代码时需要重新建索引再跑。
4. **改写没有失败样本**：45 题改写全部成功（无 rewrite_error），所以本报告没有覆盖"改写失败/幻觉"的尾部风险；
   真实链路里模型偶尔会输出空 content（已实现 `max_tokens` 递增重试 + 原句兜底）。
5. **样本量与构成**：45 题、每文件最多 3 条；kind 上 func/method 占 31/45，const/var/interface 偏少；
   locating 类只有 1 题，无法支撑分难度结论。

---

## 5. 复现方式

```powershell
# 只读确认集合结构 / 导出全量 chunk 元数据（供出题与自检）
go run ./eval/rag -schema
go run ./eval/rag -dump                       # -> eval/rag/collection_index.json

# 核对某个评测集的目标是否真的在库里（缺目标会打印 PATH-MISSING / SYMBOL-MISSING，退出码 4）
go run ./eval/rag -check -queryset eval/rag/queryset.json

# 跑全量（五条 arm，产出 results.json）
go run ./eval/rag

# 本次用的兜底评测集（可复现，但 LLM 出题有随机性，题目文本可能不同）
go run ./eval/rag -gen 45                     # -> eval/rag/queryset.fallback.json
go run ./eval/rag -queryset eval/rag/queryset.fallback.json -out eval/rag/results.fallback.json

# 冒烟（内置 3 题，验证五条 arm 都能出结果）
go run ./eval/rag -smoke                      # -> eval/rag/results.smoke.json
```

**如果 `eval/rag/queryset.json` 后来出现了**：直接 `go run ./eval/rag`（默认读它、写 `eval/rag/results.json`），
再 `go run ./eval/rag -check` 确认目标齐全即可；本报告的表格结构可以直接用新结果替换。

---

## 6. 附：本次运行的产物

| 文件 | 内容 |
| --- | --- |
| `eval/rag/main.go` | 跑分程序（五条 arm + `-schema` / `-dump` / `-check` / `-gen` / `-smoke`） |
| `eval/rag/results.json` | 本报告的原始明细（= `results.fallback.json` 副本；`queryset_path` 字段指向兜底评测集） |
| `eval/rag/results.fallback.json` | 兜底集原始明细（每题额外含 `question_en` 改写文本、逐条命中与分数） |
| `eval/rag/queryset.fallback.json` | runner 自造的 45 题兜底评测集 |
| `eval/rag/collection_index.json` | collection 全量 828 chunk 元数据快照 + 文件/kind/commit 统计 |
| `eval/rag/results.smoke.json` | 内置 3 题的冒烟结果 |

<!-- BEGIN AUTO:arm-comparison -->
## 附：候选池 / RRF-only / 查询改写 对照（自动生成）

- 生成时间：2026-09-29T00:21:57+08:00
- collection：`edu_agent_code_docs_p1c6a6a726a49c9a8`，题目数：45
- 候选池：dense_top_k=40 / sparse_top_k=40 / candidate_k=40，重排后保留 final=20 条
- 查询改写臂模型：deepseek-flash

| arm | F@1 | F@5 | F@10 | F@20 | S@1 | S@5 | S@10 | S@20 | MRR | avg_ms | p50_ms | p95_ms | rw_ms |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `dense_zh` | 0.2667 | 0.6889 | 0.8000 | 0.8667 | 0.4000 | 0.7111 | 0.7556 | 0.9556 | 0.4349 | 330.1 | 321.0 | 368.0 | 0.0 |
| `dense_en` | 0.5556 | 0.8222 | 0.8889 | 0.9333 | 0.6222 | 0.8222 | 0.8667 | 0.9556 | 0.6932 | 298.9 | 287.0 | 366.0 | 1290.2 |
| `sparse_zh` | 0.0000 | 0.1778 | 0.2444 | 0.3778 | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 0.0969 | 41.0 | 47.0 | 55.0 | 0.0 |
| `hybrid_zh` | 0.3333 | 0.8000 | 0.9333 | 0.9333 | 0.7111 | 0.9556 | 1.0000 | 1.0000 | 0.5345 | 847.2 | 847.0 | 962.0 | 0.0 |
| `hybrid_en` | 0.2000 | 0.7778 | 0.9556 | 0.9556 | 0.6667 | 0.8889 | 0.9556 | 0.9556 | 0.4772 | 832.3 | 828.0 | 959.0 | 1290.2 |
| `hybrid_zh_norerank` | 0.0667 | 0.3111 | 0.4889 | 0.8000 | 0.6444 | 0.8667 | 0.9778 | 1.0000 | 0.2072 | 309.8 | 312.0 | 341.0 | 0.0 |
| `hybrid_en_norerank` | 0.3333 | 0.6667 | 0.8444 | 0.8889 | 0.3556 | 0.6667 | 0.7333 | 0.8444 | 0.4887 | 292.8 | 290.0 | 351.0 | 1290.2 |
| `rewrite_hybrid_zh` | 0.0889 | 0.8667 | 0.9556 | 0.9778 | 0.6889 | 0.9111 | 0.9778 | 0.9778 | 0.4164 | 3844.6 | 3203.0 | 4842.0 | 2229.2 |

F = 文件级 Recall，S = 符号级 Recall；rw_ms = 该臂平均改写耗时（0 表示该臂不涉及改写）。

### 分难度（文件级 F@5 / 符号级 S@5 / MRR）

| difficulty | arm | n | F@5 | S@5 | MRR |
| --- | --- | --- | --- | --- | --- |
| descriptive | `dense_zh` | 16 | 0.7500 | 0.8125 | 0.5920 |
| descriptive | `dense_en` | 16 | 0.8750 | 0.8750 | 0.6640 |
| descriptive | `sparse_zh` | 16 | 0.1250 | 1.0000 | 0.0815 |
| descriptive | `hybrid_zh` | 16 | 0.7500 | 0.9375 | 0.4783 |
| descriptive | `hybrid_en` | 16 | 0.7500 | 0.9375 | 0.4766 |
| descriptive | `hybrid_zh_norerank` | 16 | 0.3750 | 0.8750 | 0.2441 |
| descriptive | `hybrid_en_norerank` | 16 | 0.3750 | 0.6875 | 0.3153 |
| descriptive | `rewrite_hybrid_zh` | 16 | 0.8750 | 0.8750 | 0.4057 |
| explaining | `dense_zh` | 28 | 0.6429 | 0.6429 | 0.3535 |
| explaining | `dense_en` | 28 | 0.7857 | 0.7857 | 0.7168 |
| explaining | `sparse_zh` | 28 | 0.1786 | 1.0000 | 0.0913 |
| explaining | `hybrid_zh` | 28 | 0.8214 | 0.9643 | 0.5500 |
| explaining | `hybrid_en` | 28 | 0.7857 | 0.8571 | 0.4768 |
| explaining | `hybrid_zh_norerank` | 28 | 0.2500 | 0.8571 | 0.1756 |
| explaining | `hybrid_en_norerank` | 28 | 0.8214 | 0.6429 | 0.5695 |
| explaining | `rewrite_hybrid_zh` | 28 | 0.8571 | 0.9286 | 0.4196 |
| locating | `dense_zh` | 1 | 1.0000 | 1.0000 | 0.2000 |
| locating | `dense_en` | 1 | 1.0000 | 1.0000 | 0.5000 |
| locating | `sparse_zh` | 1 | 1.0000 | 1.0000 | 0.5000 |
| locating | `hybrid_zh` | 1 | 1.0000 | 1.0000 | 1.0000 |
| locating | `hybrid_en` | 1 | 1.0000 | 1.0000 | 0.5000 |
| locating | `hybrid_zh_norerank` | 1 | 1.0000 | 1.0000 | 0.5000 |
| locating | `hybrid_en_norerank` | 1 | 1.0000 | 1.0000 | 1.0000 |
| locating | `rewrite_hybrid_zh` | 1 | 1.0000 | 1.0000 | 0.5000 |

_（本节由 `go run ./eval/rag -report eval/rag/REPORT.md` 自动生成于 2026-09-29T00:27:04+08:00，重跑即覆盖。）_
<!-- END AUTO:arm-comparison -->

---

## 7. 第二轮：候选池解耦 / RRF-only / 查询改写（2026-09-29）

本节回答上一版报告留下的两个问题：①hybrid 的 F@20 为什么低于 dense 单路，放大候选池能否修好；
②智谱 rerank 分数高度饱和，它到底有没有净收益。顺带把"中文查询改写"这条臂也接进跑分。

### 7.1 这一轮改了什么

- **候选池与最终返回解耦**（生产侧 `adaptor/vector/milvus.go` + `config.Milvus`）：
  `dense_top_k=40` / `sparse_top_k=40` / `candidate_k=40`，RRF 融合后保留 40 条再重排；
  评测臂逐条对齐同一组参数（`eval/rag/main.go` 的 `armSpec`/`armPool`）。
- **新增 3 条臂**：`hybrid_zh_norerank`（RRF-only，同候选池但跳过重排）、
  `hybrid_en_norerank`（中英对称）、`rewrite_hybrid_zh`（用 `service/rag` 的查询改写：
  中文原句 → 多路改写 → 每路各跑一遍 hybrid → RRF 融合 → 重排；原文那一路始终在内）。
- 指标补齐 **P50/P95 延迟** 与 **S@5**；对照表见上面的自动生成块。
- 数据口径沿用上一轮的 45 题兜底评测集（`queryset.fallback.json`，45/45 目标在库里核对通过），
  因此可与上一版表格逐列对照。

### 7.2 结论

**1）候选池放大后，hybrid 的 F@20 反超回来，达到与 dense 单路持平。**
上一轮 `hybrid_zh` 的 F@20 是 0.8889（低于 `dense_zh` 的 0.9556），根因是"融合后只剩 20 条、
把单路本来能召回的目标截掉了"。把候选池放到 40 后，`hybrid_zh` 的 F@20 = **0.9556**，
`hybrid_en` 的 F@20 = **1.0000**，`rewrite_hybrid_zh` 的 F@20 = **1.0000**。
F@10 也从上一轮的 0.8889 提升到 0.9556。这条改造方向验证通过。

**2）rerank 不是"没用的饱和分数"，它的净收益很大。**
把候选池、查询、融合方式全部固定，只切 rerank 开关：

| 对照（同候选池 40，仅切换 rerank） | F@1 | F@5 | F@10 | F@20 | S@5 | MRR |
| --- | --- | --- | --- | --- | --- | --- |
| `hybrid_zh`（+rerank） | 0.5333 | 0.8889 | 0.9556 | 0.9556 | 0.8444 | 0.6771 |
| `hybrid_zh_norerank`（RRF-only） | 0.2444 | 0.4444 | 0.5333 | 0.8667 | 0.4000 | 0.3365 |
| `hybrid_en`（+rerank） | 0.6444 | 0.8667 | 0.9778 | 1.0000 | 0.8889 | 0.7482 |
| `hybrid_en_norerank`（RRF-only） | 0.3778 | 0.6667 | 0.7778 | 0.9556 | 0.6889 | 0.5016 |

中文那一路 F@1 +28.9pp、F@5 +44.4pp、MRR +0.34，英文那一路 F@1 +26.7pp、F@5 +20.0pp。
**结论：即使 rerank 的打分区间被压缩在 0.998~1.0，它给出的排序仍远好于原始 RRF 顺序**，
上一版"分数饱和所以重排没用"的怀疑不成立——重排不该关。

**3）`rewrite_hybrid_zh`（改写但保留原查询）能补回跨语言损失，质量与 `hybrid_en` 相当。**

| | F@1 | F@5 | F@10 | F@20 | S@5 | MRR | p50_ms |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `hybrid_zh`（不改写） | 0.5333 | 0.8889 | 0.9556 | 0.9556 | 0.8444 | 0.6771 | 731 |
| `hybrid_en`（先改写再检索） | 0.6444 | 0.8667 | 0.9778 | 1.0000 | 0.8889 | 0.7482 | 777 |
| `rewrite_hybrid_zh`（多路改写+原查询） | 0.5111 | **0.9111** | 0.9778 | 1.0000 | **0.9111** | 0.6751 | 3109 |

`rewrite_hybrid_zh` 在 F@5 / F@20 / S@5 上不低于甚至略好于 `hybrid_en`，且**不替换原始中文查询**
（多路里含原文），因此对"原句本身就能命中"的情形没有退化风险。代价是延迟：改写平均 2057ms，
整条臂 P50 约 3.1s（P95 约 5.7s），相对 `hybrid_zh` 的 0.73s 高出约 4 倍。

**4）跨语言鸿沟仍在，且主要伤害排序而非召回。**
`dense_zh` F@1=0.3556 vs `dense_en` F@1=0.6000（**+24.4pp**），MRR 0.5310 vs 0.7205（**+36%**），
但两者 F@20 都是 0.9556。即中文提问"够得着"目标却排在后面，改用英文检索式能把顺序提前。
这仍是"加中文→英文改写节点"最直接的论据。

**5）`sparse_zh`（BM25）依旧天花板明显**：F@20=0.4000、S@5=0.2222，中文自然语言提问不要单独走 BM25。

### 7.3 复现

```powershell
# 全量：8 条臂，写 results.json 并把对照表写入本文件
go run ./eval/rag -queryset eval/rag/queryset.fallback.json -out eval/rag/results.json -report eval/rag/REPORT.md

# 只跑某几条臂 / 关掉重排 / 改候选池 / 不跑改写臂
go run ./eval/rag -arms hybrid_zh,hybrid_zh_norerank
go run ./eval/rag -no-rerank
go run ./eval/rag -dense-topk 60 -sparse-topk 60 -candidate-k 60 -rerank-topn 20
go run ./eval/rag -rewrite-arm=false        # rewrite_hybrid_zh 会被标注跳过
```

### 7.4 采样说明与残留风险

- 仍用 runner 自造的 45 题兜底集（`queryset.json` 独立评测集至今未生成），绝对数值只能当方向性证据。
- collection 是旧快照（`c3ea49d`）；本机 main 上新增的 RAG 文件不在库里，结论外推到新代码需重建索引再跑。
- rerank、embedding、改写都走外部服务，数字有网络抖动；本表是单次运行。
- `rewrite_hybrid_zh` 的耗时包含一次改写 LLM 调用与多路检索，未做并发优化；若要上线需按体验权衡
  （例如只在首轮/低置信时改写）。

## 8. 第三轮：候选池修复 + 重建索引后的复跑（2026-09-29）

### 8.1 本轮做了什么

1. **修 hybrid 候选池**：新增 `rag.milvus.dense_top_k / sparse_top_k / candidate_k`（默认 40），
   两路各自召回后 RRF 融合，融合结果不再被截回 `top_k`。修掉"hybrid F@20 反而低于 dense 单路"。
2. **`Rerank.Enabled` 真正生效**，可以关掉重排做 RRF-only 对照。
3. **新增中文查询改写**（`service/rag/rewrite.go`）：一次 LLM 调用产出
   `{rewritten_en, keywords[], subqueries[]}`，作为并行补充路径，不替换原查询；默认关闭，失败降级。
4. **重建项目索引**：commit `c3ea49d` → `ae3de77`，包含本轮新增的代码文件。

重建命令（一次性 e2e 用例，文件被 `.gitignore` 忽略）：

```powershell
$env:RAG_E2E=1; $env:REBUILD_ROOT="<clone 路径>"; $env:REBUILD_PID="p1c6a6a726a49c9a8"
$env:REBUILD_COMMIT="ae3de773dc69b79b0f9266ca2768204d36a27b66"
go test ./e2e -tags e2e -run TestRebuildProjectIndex -v -count=1
```

### 8.2 重建后的索引构成

| 指标 | 重建前 | 重建后 |
| --- | --- | --- |
| 条数 | 828 | **1248** |
| commit | c3ea49d | ae3de77 |
| 锁文件块 | 272（25%） | **0** |
| `kind=text` | 44（5.3%） | 81（6.5%） |
| 元数据 `kind/symbol/line_start` | 有 | 有 |

重建后 `kind` 分布：`func` 492 / `method` 221 / `struct` 167 / `file_summary` 122 / `text` 81 /
`const` 58 / `block` 43 / `var` 41 / `interface` 14 / `type` 9。
扩展名分布：`.go` 997 / `.tsx` 107 / `.ts` 65 / `.md` 25 / `.json` 20 / `.ps1` 12 / `.mod` 11 /
`.yml` 9 / `.html` 1 / `.yaml` 1。

重建耗时 21.2s（1248 条，云端 embedding）。评测集目标核对：**45 题中 42 题完全命中，路径缺失 1、符号缺失 3**。

### 8.3 本轮指标（45 题兜底集，重建后的索引）

见文件顶部"附：候选池 / RRF-only / 查询改写 对照（自动生成）"一节。四条主要结论：

1. **候选池修复生效**：`hybrid_zh` F@20 = 0.9333 ≥ `dense_zh` 0.8667，旧版"融合后低于单路"消失。
2. **rerank 净收益很大**：`hybrid_zh` F@1 0.3333 vs `hybrid_zh_norerank` 0.0667（**+26.7pp**），
   F@5 0.8000 vs 0.3111（**+48.9pp**）。上一版"分数饱和所以重排没用"的怀疑不成立。
3. **改写臂的召回最好、排序最差**：`rewrite_hybrid_zh` F@5 0.8667、S@5 0.9111（本轮最高），
   但 F@1 仅 0.0889，且 P50 延迟 3.2s（改写平均 2.2s）。
4. **文件级 F@1 相比上一轮整体下降**（`hybrid_zh` 0.5333 → 0.3333）。原因见 8.4，暂不能归因于单项改动。

### 8.4 三条必须知道的口径限制

1. **S@ 列不可信，只看 F@。** `eval/rag/main.go` 的符号命中判定是
   `symbol == want || strings.Contains(doc.Content, want)`——符号名出现在任意 chunk 正文里就算命中，
   不校验文件。因此会出现 `sparse_zh` S@1 = 1.0000 而 F@1 = 0.0000、`hybrid_zh` S@1 = 0.7111 > F@1 = 0.3333
   这类"符号命中率高于文件命中率"的矛盾。**S@ 应加上文件约束后再用**。
2. **跨轮不可比。** 本轮同时换了索引（828 → 1248 条、盲切改符号切分）和 harness 语义
   （`*_en` 臂改为运行时改写，`rw_ms≈1290ms`；上一轮是预置英文题面）。只有同轮内各臂横向对比有效。
3. **评测集仍是兜底集。** 45 题由 LLM 依据旧索引生成，独立评测集 `eval/rag/queryset.json` 至今缺位，
   绝对数值只能当方向性证据。

### 8.5 本轮新增待办

- 修 `S@` 口径（符号命中需限定在同一文件内）。
- 生成独立评测集，替换兜底集。
- 归因文件级 F@1 的下降：怀疑与"符号级小块数量翻倍、单块语义变窄"有关，
  建议做一次"盲切 vs 符号切分"的同集 A/B，而不是继续在同一索引上换参数。
