# 陌生仓库检索验证：axios/axios

**状态：已完成（2026-09-29 02:12）。** 上一轮阻塞的智谱 embedding 配额已恢复，8 条 arm 全部跑通。

核心问题「F@10 在陌生仓库上还在不在 0.9 量级」的答案是：**不在。** 本仓库上 F@10 = 0.9333，
axios 上最好的一条 arm 只有 **0.70**（`dense_en`）；F@1 从 0.6444 掉到 **0.35**。

---

## 0. 阻塞解除的证据

上一轮的 429（`余额不足或无可用资源包`）已消失。用 `agent_code_local.yml` 里同一个 key 直连探针：

```
POST https://open.bigmodel.cn/api/paas/v4/embeddings
{"model":"embedding-3","input":"hello world","dimensions":2048}
-> 200 OK
```

本轮 20 题的 5 条 embedding arm **零错误**（`errors=0`），改写调用也 20/20 成功。

---

## 1. 索引快照

| 项 | 值 |
| --- | --- |
| 仓库 | `axios/axios`（github，depth=1） |
| commit | `2426e03ba9020be31ed013873423cea6b7cd2e67` |
| 克隆路径 | `workspace/repos/axios-axios` |
| project_id | `p24e1f35abce5e675` |
| collection | `edu_agent_code_docs_p24e1f35abce5e675` |
| chunk 数 | 3253（口径见 `eval/rag/collection_index.axios.json`：`row_count` 与 chunks 长度一致，无重复计数） |
| 文件数（进索引，去重路径） | 432 |
| 索引耗时 | 55.9 s |
| collection schema | `id` / `content`(analyzer=chinese) / `metadata`(JSON) / `vector`(dim=2048) / `sparse_vector`(bm25) |

评测集：`eval/rag/queryset.axios.json`，20 题，来源是**已合并 PR 的标题/描述**（真实人类语言），
ground truth = 该 PR 改动的主文件。`-check` 核对 **20/20 完全命中，路径缺失 0**。
20 个目标**全部是源码文件**（`lib/adapters/*.js`、`lib/core/*.js`、`lib/helpers/*.js`、`index.d.ts`），
没有一题的目标是 `.md`。这一点很关键：下文所有"命中文档"都是纯噪声。

---

## 2. 指标（20 题，文件级）

| arm | F@1 | F@5 | F@10 | F@20 | MRR | p50 (ms) | p95 (ms) | 改写 (ms) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `dense_zh` | 0.2000 | 0.4500 | 0.6000 | 0.7000 | 0.3252 | 293 | 525 | — |
| **`dense_en`** | **0.3500** | **0.5500** | **0.7000** | 0.7500 | **0.4252** | 281 | 334 | 2002 |
| `sparse_zh` | 0.0000 | 0.0000 | **0.0000** | **0.0000** | 0.0014 | 27 | 46 | — |
| `hybrid_zh` | 0.3000 | 0.6000 | 0.6500 | 0.7000 | 0.4054 | 847 | 898 | — |
| `hybrid_en` | 0.1500 | 0.4500 | 0.6000 | 0.7500 | 0.2874 | 870 | 936 | 2002 |
| `hybrid_zh_norerank` | 0.0000 | 0.1500 | 0.4500 | 0.6000 | 0.1163 | 304 | 341 | — |
| `hybrid_en_norerank` | 0.0500 | 0.4000 | 0.6000 | **0.8000** | 0.2058 | 280 | 330 | 2002 |
| `rewrite_hybrid_zh` | 0.2000 | 0.5500 | 0.6000 | 0.7000 | 0.3106 | 3075 | 3728 | 1688 |

对照（本仓库，同一 harness、同一口径，取自 `eval/rag/REPORT.md`）：

| arm | F@1 | F@5 | F@10 | MRR |
| --- | --- | --- | --- | --- |
| 本仓库 `dense_en` | 0.6444 | 0.8889 | 0.9111 | 0.7395 |
| 本仓库 `hybrid_zh` | 0.4444 | 0.8444 | **0.9333** | 0.6181 |
| axios `dense_en` | 0.3500 | 0.5500 | **0.7000** | 0.4252 |
| axios `hybrid_zh` | 0.3000 | 0.6000 | 0.6500 | 0.4054 |

**F@10 掉 23 个百分点（0.9333 → 0.7000），F@1 掉 29 个百分点（0.6444 → 0.3500）。**
本仓库那些"0.9 量级"的结论**不能直接外推**到正常的 JS 库：本仓库 `.go` 占 80.8%，
是唯一走真 AST 精确切分的语言，整体绝对数字偏乐观——`REPORT.md` 第 5.4 条已经写过这一点，这次拿到了外部对照。

---

## 3. 三条证据

### 3.1 中文 BM25 单路在陌生仓库上**完全失效**（不是变弱，是归零）

`sparse_zh` 的 F@1/F@5/F@10/F@20 全是 **0.0000**，MRR 0.0014。
把它的 top-20（20 题 × 20 = 400 个槽位）拆开看：

| 观察 | 值 |
| --- | --- |
| 落在 `docs/zh/` 中文文档的槽位 | 398 / 400 |
| 落在非 `.md` 且非 `docs/` 的槽位 | **2 / 400** |
| 出现次数最多的文件 | `docs/zh/pages/advanced/request-config.md`，**200 次**（20 题每题 rank-1 都是它） |
| 不同文件数 | 34 |

axios 是双语仓库，`docs/zh/` 下全是中文散文；中文 query 与中文散文的逐字重合度远高于英文源码，
于是 BM25 把每一题的第一名都判给了同一份中文配置文档。**换到没有中文文档的仓库结论会不同**，
但只要仓库里有中文文档（国内项目很常见），中文提问下 BM25 就是纯噪声源——而它是被等权融进 hybrid 候选池的。

### 3.2 文档过采被量化：融合会把噪声放大，重排才能压回去

统计各 arm 的 top-10 槽位（每题 10 条 × 20 题 = 200 槽）里，`.md` 文件和 `docs/` 目录的占比：

| arm | `docs/` 占比 | `.md` 占比 | 源码占比 |
| --- | --- | --- | --- |
| `sparse_zh` | **100%** | **100%** | 0% |
| `hybrid_zh_norerank` | **74%** | **76%** | 24% |
| `hybrid_zh` | 40% | 42% | 57% |
| `dense_zh` | 41% | 44% | 56% |
| `rewrite_hybrid_zh` | 33% | 46% | 54% |
| `hybrid_en` | 25% | 41% | 58% |
| `dense_en` | **17%** | **30%** | **69%** |

两个直接结论：

- **RRF 融合确实放大噪声**：`hybrid_zh_norerank` 的 top-10 有 74% 是文档，比单路 `dense_zh` 的 41% 还高——
  因为 BM25 那一路（100% 文档）被等权融合进来了。**重排把它压回 40%**，这是重排在陌生仓库上的主要价值。
- **英文改写天然绕开噪声**：`dense_en` 只有 17% 的槽位是文档。

**离线反事实**（不改索引，只把 top-20 里的 `.md` / `docs/` 槽位全部剔除后按原序重新编号）：

| arm | F@1 | F@5 | F@10 | F@20 |
| --- | --- | --- | --- | --- |
| `dense_zh` | 0.2000 → 0.2500 | 0.4500 → 0.5500 | 0.6000 → 0.6500 | 0.70 |
| `dense_en` | 0.3500 → 0.4000 | 0.5500 | 0.7000 → 0.7500 | 0.75 |
| `hybrid_zh_norerank` | 0 → 0.2500 | 0.1500 → 0.5500 | 0.4500 → 0.6000 | 0.60 |
| `hybrid_en_norerank` | 0.0500 → 0.1500 | 0.4000 → 0.5500 | 0.6000 → **0.8000** | 0.80 |
| `hybrid_en` | 0.1500 → 0.2000 | 0.4500 → 0.6500 | 0.6000 → 0.7500 | 0.75 |

**去掉全部文档，F@10 天花板也只到 0.75~0.80**。所以"文档过采"是真实损耗（+5~20 pp，
在 `hybrid_*_norerank` 上最大），但**不是**陌生仓库掉 23 个点的主因——主因是跨语言鸿沟加上源码侧排序本身不够。

### 3.3 切分质量：先纠正上一轮的一个错误推断，再给出真正的问题

**纠正**：上一版报告根据 "`kind=text` 占 43.8%（本仓库只有 5.3%）" 推断
"JS/TS 大括号启发式回退率高一个数量级"。**这个推断是错的。** 按扩展名交叉统计 `text` 块的来源：

| `text` 块的来源 | 数量 |
| --- | --- |
| `.md` | **1316** |
| `.html` | 36 |
| `.yml` | 33 |
| `.js` | **21** |
| `.json` | 17 |
| `.ts` | 2 |

`text` 块 98% 是 Markdown/HTML/YAML 这类非代码文件的盲切，**JS 的真实回退是 21 / 1620 ≈ 1.3%，比本仓库的 5.3% 还低**。
"43.8% text" 反映的是**文档占比高**（见 3.2），不是 JS 切分差。

**真正的问题在类（class）**。看 `lib/core/Axios.js` 这一个文件切出来的 11 个 chunk：

| kind | symbol | 行范围 |
| --- | --- | --- |
| type | Axios | 16–56 |
| type | Axios | 50–84 |
| type | Axios | 75–110 |
| type | Axios | 105–146 |
| type | Axios | 138–176 |
| type | Axios | 171–202 |
| type | Axios | 197–236 |
| type | Axios | 231–266 |
| block | （空） | 268–280 |
| func | generateHTTPMethod | 282–307 |
| file_summary | （空） | 1–15 |

**整个类被切成了 7 段互相重叠的窗口，符号全是同一个 `Axios`，类里的方法一个都没有被识别成独立符号。**
（全库 `.js` 只有 `block` / `func` / `type` / `file_summary` 四种 kind，**没有 `method`、没有 `class`**；
本仓库 Go 侧有 144 个 `method` 块。）后果是**文件内部的区分度被抹平**：不管是 `Axios.prototype.request`
还是 `interceptors`，检索阶段看到的都是同一个 symbol、同一个 header。

这不是理论推测。ground truth 是 `lib/core/Axios.js` 的 3 道题（a002/a010/a017），各 arm 的首中名次：

| 题 | `dense_zh` | `dense_en` | `hybrid_zh` | `hybrid_en` | `hybrid_zh_norerank` | `hybrid_en_norerank` |
| --- | --- | --- | --- | --- | --- | --- |
| a002 | miss | miss | miss | miss | miss | miss |
| a010 | 38 | miss | miss | miss | miss | miss |
| a017 | 13 | 10 | 17 | miss | 22 | 20 |

**三道题里没有一次进过 top-5**，基本只能靠 20 名外捞回来。切分粒度不够细，是"召回够、排序烂"的物理来源之一。

---

## 4. 结论

1. **核心问题答复：F@10 在陌生仓库上不在 0.9 量级，实测最好 0.70。** 本仓库 0.9333 → axios 0.7000（−23 pp），
   F@1 0.6444 → 0.3500（−29 pp）。开发期回归集上的分数不能外推。
2. **英文改写（`dense_en`）依然是陌生仓库上最强的单条路径**，也是唯一同时拿下 F@1/F@5/F@10/MRR 的 arm。
   与本仓库 `REPORT.md` 3.2 的结论一致，只是绝对值也掉了一半。
3. **中文 BM25 单路在含中文文档的仓库上是纯噪声**（F@ 全 0，top-20 里 398/400 是 `docs/zh/` 中文文档）。
   它被等权融进 hybrid，是 RRF-only 掉分的直接原因；重排是当前唯一能压住它的环节。
4. **多路改写（`rewrite_hybrid_zh`）没有收益**：F@1 0.20 / F@10 0.60，全面弱于单句英文 `dense_en`，
   而 p50 延迟 3075 ms 是它的 11 倍。多路融合在这个仓库上只增加成本。
5. **文档过采是真实损耗但不是主因**：剔除全部 `.md` 后 F@10 天花板 0.75~0.80，仍不到 0.9。
6. **JS 类切分是下一个能直接提分的地方**：类被切成重复符号的重叠窗口、没有方法级块，
   已有"同一个文件的 3 道题连续打不中"作为直接证据。

---

## 5. 复现

```powershell
# 建索引（collection 已存在，只有重建才需要）
git clone --depth=1 https://github.com/axios/axios.git workspace/repos/axios-axios
go run ./tmp_unseen_index "C:\FullStack\Code Analyse Agent\workspace\repos\axios-axios"

# 核对评测集目标
go run ./eval/rag -collection edu_agent_code_docs_p24e1f35abce5e675 `
  -queryset eval/rag/queryset.axios.json -check

# 全量 8 条 arm（本轮命令，产出 eval/rag/results.axios.json）
go run ./eval/rag -collection edu_agent_code_docs_p24e1f35abce5e675 `
  -queryset eval/rag/queryset.axios.json -out eval/rag/results.axios.json
```

## 6. 遗留

- 上一轮失败运行留下的 `eval/rag/results.axios.sparse.json`（只有 `sparse_zh` 成功的那次）已被本轮全量结果取代，
  保留它只为对照，可随时删除。
- 符号级 `S@k` 在本轮全为 0，因为评测集 `target_symbol` 一律留空（该口径已废弃，见 `REPORT.md` 5.1）；只看 `F@k` 与 `MRR`。
- `tmp_unseen_index/` 是临时建索引程序，收尾时删除。
- 本报告只覆盖 axios 一个仓库；要坐实"陌生仓库普遍约 0.7"还需要第二个不同技术栈的仓库
  （建议 Python 或 Java，避开 JS 类切分这个已定位的干扰项）。
