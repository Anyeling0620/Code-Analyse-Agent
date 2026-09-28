# RAG 端到端链路验证

验证目标：**用户给一个 git 地址 → 克隆到 workspace → 语义索引写入 Milvus →
分析侧 agent 能调用 `rag_retriever` → 用它回答跨文件关联问题**。

## 结论摘要

链路**是通的**，四段都有实测证据。但过程中发现并修复了**一个会静默摧毁索引的
链路级 bug**（见第 5 节）——在修之前，"服务重启后第一次使用 RAG"会得到一个
空集合，而状态接口仍报 ready。

链路的**机制**可用；链路的**检索质量**在真实仓库上仍受限于散文过采（见第 6 节）。

## 1. repo_fetch：把 git 地址变成 workspace 里的稳定目录

项目自带的 e2e 测试 `e2e/repo_rag_test.go`（`//go:build e2e`）覆盖这一段。
实跑命令与结果：

```
$env:RAG_E2E="1"
go test ./e2e/ -tags e2e -run TestRepoFetchAndProjectRAG -v -count=1
--- PASS: TestRepoFetchAndProjectRAG (16.52s)
```

原始输出（节选）：

```
repo_fetch(https://github.com/golang/example.git) 返回:
  root=C:\FullStack\Code Analyse Agent\workspace\repos\github.com-golang-example-pd27d131ebc169e00
  project_id=pd27d131ebc169e00
  remote=https://github.com/golang/example.git
  commit=7f05d217867b2af52b0a28c6d1c91df97e1b5b39
  branch=master   reused=true   size_mb=0.43   file_count=71

repo_fetch(https://github.com/octocat/Hello-World.git) 返回:
  root=...\workspace\repos\github.com-octocat-Hello-World-pf7c3f8c9bae246cb
  commit=7fd1a60b01f91b314f59955a4e4d4e80d8edf11d   file_count=1
```

要点：目录名稳定可复现（`host-owner-repo-<hash8>`）；同一仓库二次调用
`reused=true` 幂等复用；无扩展名的 `README` 也被正确计入（`file_count=1`
的仓库若按扩展名过滤会得到空索引）。

## 2. 存入 Milvus

同一测试内，`repo_fetch` 完成后的异步索引结果：

```
[INFO] project rag index ready project_id=pd27d131ebc169e00 chunks=453 elapsed=11.23s
[INFO] project rag index ready project_id=pf7c3f8c9bae246cb chunks=1   elapsed=3.28s

repoA 索引状态: {ProjectID:pd27d131ebc169e00 Status:ready Commit:7f05d217... ChunkCount:453}
repoB 索引状态: {ProjectID:pf7c3f8c9bae246cb Status:ready Commit:7fd1a60b... ChunkCount:1}
```

metadata 字段实测齐备（axios 集合 3353 条全量导出）：

```
commit_sha / header / id / kind / line_end / line_start / source_path / symbol
```

另有一个大集合的抽样（`axios/axios`，3353 条）：

| 项 | 值 |
| --- | --- |
| collection | `edu_agent_code_docs_p24e1f35abce5e675` |
| 行数 | 3353 |
| kind 分布 | block 784 / file_summary 219 / func 573 / **method 137** / text 1426 / type 214 |
| 索引耗时 | 60.4s |

## 3. 检索工具确实挂在分析侧 agent 上

装配路径（代码证据）：

```
service/conversation/depends.go:92-112
    projectIndexer.Tool(ctx)  → ragTool（项目级 rag_retriever）
    tools.analysis = append(tools.analysis, ragTool)
    tools.qa       = append(tools.qa, ragTool)

service/conversation/depends.go:199-200
    WithAnalysisTool(tools.analysis).WithQaTool(tools.qa)

service/agent/runner/runner.go:195-209  buildAnalyzerAgent()
    repo_analyzer.NewAnalyzerWithOptions(ctx, chatModel, c.analysisTool, ...)

service/agent/runner/runner.go:213-222  buildProjectQaAgent()
    project_qa.NewQaAgentWithOptions(ctx, chatModel, c.qaTool, ...)
```

即：**`repo_analyzer`（分析主链路）与 `project_qa`（追问）都拿得到
`rag_retriever`**，且工具名对模型保持不变（`projectRetrieverToolName = "rag_retriever"`），
按会话上下文解析"当前分析的是哪个项目"，未就绪时返回可读提示而不是报错。

集成层的直接证据（e2e 测试内实际调用检索）：

```
project=pd27d131ebc169e00 query="readme example project" 命中 5 条，第一条 source=slog-handler-guide/Makefile
project=pf7c3f8c9bae246cb query="hello world"            命中 1 条，第一条 source=README

项目 pd27d131ebc169e00 使用独立集合: edu_agent_code_docs_pd27d131ebc169e00
项目 pf7c3f8c9bae246cb 使用独立集合: edu_agent_code_docs_pf7c3f8c9bae246cb
```

两个项目落在两个集合，检索不串——这是"多聊天/多项目不混淆"的直接验证。

## 4. 跨文件关联（本次验证的重点）

在 `axios/axios`（3353 块）上设计 3 个必须跨文件才能回答的中文问题，
走生产同一条 ProjectIndexer → store → Retrieve 路径，`top_k = 8`：

| # | 问题 | top-5 命中文件 | 去重文件数 |
| --- | --- | --- | ---: |
| 1 | HTTP 请求从对外接口发出到真正建立网络连接，跨了哪几个文件？ | `docs/zh/pages/advanced/error-handling.md`、`sandbox/client.html`、`tests/unit/adapters/xhr.test.js`×2、`tests/browser/options.browser.test.js` | **4** |
| 2 | 请求拦截器和响应拦截器分别是在哪里被应用的？ | `README.md`、`docs/es/.../interceptors.md`、`docs/fr/.../interceptors.md`、`docs/pages/advanced/interceptors.md`×2 | **4** |
| 3 | 默认请求配置是怎么在不同运行时之间统一的？ | `docs/zh/.../create-an-instance.md`、`docs/pages/advanced/request-config.md`、`PRE_RELEASE_CHANGELOG.md`、`docs/zh/.../config-defaults.md`×2 | **4** |

每题的结果都覆盖 ≥2 个文件（实际都是 4 个），**跨文件关联成立**。

内容与行号可信性：对带 `line_start` 的命中逐条回读源文件比对，全部一致：

```
CHAIN_HIT [3] tests/unit/adapters/xhr.test.js lines=24-41 symbol="StubXMLHttpRequest.open, ..."
    CHAIN_SRC_OK 内容与源文件 24-41 行一致
CHAIN_HIT [4] tests/unit/adapters/xhr.test.js lines=9-9  symbol="StubXMLHttpRequest"
    CHAIN_SRC_OK 内容与源文件 9-9 行一致
CHAIN_HIT [5] tests/browser/options.browser.test.js lines=5-5 symbol="MockXMLHttpRequest"
    CHAIN_SRC_OK 内容与源文件 5-5 行一致
```

## 5. ⚠️ 发现并修复的链路级 bug：一次召回会把索引清空

### 现象

重跑评测后集合有 3353 条。随后**在一个全新进程里**（模拟服务重启）调用一次
`ProjectIndexer.Retrieve`，再导出集合：

```
已导出 0 条 chunk 元数据
kind 分布：map[]
```

3353 条被清成 0 条，而且 `Status` 仍返回 `ready`——**静默失效，无任何报错**。

### 根因

```go
// Retrieve 走的是 storeFor
rebuilt, err := p.storeFor(ctx, projectID)

// storeFor 传了 WithInitCollection(true)
store, err := vector.NewMilvus(ctx, scoped,
    vector.WithInitCollection(true),
    vector.WithReranker(vector.NewReranker(p.conf.Rerank)))

// adaptor/vector/milvus.go:161  —— initCollection 为真时会先 drop
if conf.Milvus.DropBeforeIndex {   // 配置里是 true
    if ok, _ := client.HasCollection(...); ok {
        client.DropCollection(...)
    }
}
```

`drop_before_index: true` 是为了让**重建索引**从干净状态开始，这个意图没错；
错的是**召回路径复用了带初始化语义的构造函数**。

进程内看不出来，因为 `stores` 缓存了索引阶段创建的 store；只有在新进程里
（缓存为空、`cachedStore` 返回 nil）才会走到 `storeFor`，于是第一次召回就把
索引 drop 掉。

### 影响面

- 触发条件：**服务重启后，该项目第一次被 `rag_retriever` 检索**。
- 后果：该项目索引全丢；`Status` 仍报 ready，检索静默返回 0 条；
  模型看到的是"没有检索到相关内容"，会退回 `project_search`，用户侧无任何异常提示。
- 这否定了此前"重启后第一轮分析、第二轮追问都能复用索引"的设计假设。

### 修复

把"只读检索视图"和"索引视图"分开（`service/rag/project.go`）：

```go
// retrieverStoreFor：不传 WithInitCollection，只建 retriever 不建 indexer，
// 不会创建/重建集合；使用独立的 retrieverStores 缓存。
store, err := vector.NewMilvus(ctx, scoped,
    vector.WithReranker(vector.NewReranker(p.conf.Rerank)))
```

`Retrieve` 改走 `retrieverStoreFor`；`indexProject` 保持 `storeFor`。
两个缓存分开，避免"检索视图顶替索引视图"导致后续写入失败。

### 验证

| | 一次 `Retrieve` 之后集合里的 chunk 数 |
| --- | ---: |
| 修复前 | 3353 → **0** |
| 修复后 | 3353 → **3353** |

并且 `TestRepoFetchAndProjectRAG`（含索引 + 检索 + 双项目隔离）在修复后通过。

对应提交：`fix(rag): 召回路径不再初始化集合，避免 drop_before_index 清空索引`。

## 6. 仍然存在的缺口

1. **检索质量被散文过采压制**。第 4 节的 3 个问题里，
   Q2、Q3 的 top-5 **全是** `docs/` / `README` / `CHANGELOG`；
   Q1 命中了 3 条 `tests/` 下的 JS 文件，但**没有一条落在 `lib/` 实现目录**。
   文档确实"相关"，却不是"实现在哪"的答案。这是跨语言鸿沟之外的第二个
   系统性偏差来源，本次改动（类成员抽取）没有触及它。
2. **回退路径的 chunk 没有行号**。`.md` / `.json` / `.yml` 走通用切分器，
   `line_start = 0`，因此这类命中**无法给出 `file:line` 引用**。
   axios 集合里这样的块有 1426/3353（42.5%）。
3. **工具返回值里没有 symbol / 行号**。`service/tool/rag_retriever/tool.go`
   的 `formatDocuments` 只输出 `source=… chunk=… heading=…`，
   metadata 里已有 `symbol / line_start / line_end` 却没用上。
   因此模型拿不到"命中在哪个符号的哪几行"，只能靠 `heading`
   （例如 `method Axios.request`）间接判断，引用前仍需 `project_search` / `read_files` 核验。
4. **索引状态的内存态与集合实际内容会脱节**。`Status` 只在内存无记录时回查
   集合是否存在；集合存在但为空（例如被别的手段清空过）仍报 ready。
   第 5 节的 bug 正是靠这个盲区隐藏的。

## 7. 复现

```powershell
# 1. 链路 e2e（真实 clone + 真实 Milvus + 真实 embedding API）
$env:RAG_E2E="1"
go test ./e2e/ -tags e2e -run TestRepoFetchAndProjectRAG -v -count=1

# 2. 建/重建某个项目的索引
go run ./tmp_unseen_index "C:\FullStack\Code Analyse Agent\workspace\repos\axios-axios"

# 3. 查看集合内容（验证没被清空）
go run ./eval/rag -collection edu_agent_code_docs_p24e1f35abce5e675 `
  -dump -dump-out "$env:TEMP\axios.json"
```
