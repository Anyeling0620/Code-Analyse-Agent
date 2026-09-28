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
