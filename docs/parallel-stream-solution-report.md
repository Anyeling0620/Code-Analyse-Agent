# 跨会话并行流式输出：最终解决报告（方案选型）

- 任务：t6（integration）
- 目标站点：analyse.kuangzihao.site
- Worktree：`/root/workspace/worktrees/analyse-parallel-stream`，基线 `10f6bc0`
- **本次未部署，未触碰生产。** 交付物为工作树改动 + 构建/测试验证 + 本报告。

---

## 0. 结论先行

**做了什么**：把前端"页面级流式状态"换成"会话级流式句柄表"，并在后端补上同会话串行锁。最终语义达到验收口径——**跨会话并行**（A 在跑时 B 可独立发起并接收）、**同会话串行**（A 已有活跃 run 时再提交被拒），切走是 detach 不 abort、切回按 `lastSeq` 续接。

**为什么这么做**：调研发现后端其实**早就是 run 维度的**——`context.WithoutCancel` 已把执行与 HTTP 连接解耦、broker 按 `run_id` 分键、事件落库带 seq、续传接口按 `run_id` 挂载。真正把"并行"锁死的是前端用一个 `isStreaming` 布尔表达"整页在流式"。所以这项工作**不是补功能，而是修数据模型**。

**为什么不能用最省事的做法**：直接删掉前端的 `if (isStreaming) return` 会立刻炸——单一 `controllerRef` 会被后发的流覆盖、先结束的 `finally` 会把另一条仍在跑的会话误判为空闲、`setSessionId(payload.session_id)` 会把用户正在看的视图拽到别的会话。方案 A 因此被否决。

**最终选择**：方案 B（会话维度 RunRegistry）+ 后端进程内同会话守卫（409）。改动 16 个已跟踪文件 + **9 个新增文件**（未跟踪），前端为主，后端仅新增锁与错误码，**未改任何 API 语义或 SSE 协议**。

**验证**：web 测试 **49 例 PASS**（统计口径：`cd web && npm run test 2>&1 | grep -c '^PASS'` → `49`）、`tsc -b` exit 0（输出 0 字节）、`vite build` exit 0 **✓13.56s（主 chunk `index-C9xsr8_g.js` 462.27 kB / gzip 144.99 kB）**、`go build ./...` 0、`go vet ./...` 0、`go test` 三个受影响包全 ok。

**最大遗留风险**：同会话串行锁是**进程内**的（与 broker 同构）。当前单实例部署成立；一旦多实例，需粘性会话或把锁/广播外移（见 §6）。

---

## 1. 问题定义与验收口径

### 1.1 问题

用户在会话 A 提问、模型正在输出时，**无法切到会话 B**（侧栏会话项被禁用），也无法**新建会话**。切回 A 之前中断的输出只能靠刷新后重连补齐，体验上等于"每次只能等一条回答说完"。

### 1.2 用户确认的验收口径

| 编号 | 约束 | 含义 |
| --- | --- | --- |
| S1 | **跨会话并行** | A 的 run 在跑时，B 可独立发起新 run 并流式接收；两条 SSE、事件序列、渲染互不干扰 |
| S2 | **同会话串行** | A 已有活跃 run 时，向 A 再发一条**必须被拒**（前端提示 + 后端 409 兜底） |
| S3 | **切走是 detach，不是 abort** | 切走不中止执行；只停止更新视口，后台继续接收 |
| S4 | **切回继续接收** | 切回从 `lastSeq` 续接，事件不重不漏、不重复渲染 |
| S5 | **状态隔离** | A 的流式事件不得改变用户正在查看的会话（禁止串台） |
| S6 | **停止只作用于当前会话** | 不误停其他会话 |
| — | **不碰生产** | 只交付工作树 + 构建验证 + 本报告 |

一个容易被忽略的判定细节：**`interrupted` 不算"正在执行"**。审批中断的 run 已经没有 goroutine 在跑，用户此时可以继续提问或走 resume。所以串行锁只拦 `running`，而续传挂载用的是"可续传"语义（`running` + `interrupted`）——两者必须用不同查询，不能复用。

---

## 2. 现状盘点

> **行号基准声明**：本节（§2）所有行号指 **改造前的 main 基线 `10f6bc0`**，用于描述"改造前是什么样"。
> 改造后这些位置已经漂移（`run.go` 因新增串行闸门整体后移，`chat.go` 的 `ListMessages` 从 `:66` 移到 `:69`，`api/chat.go` 亦后移）。
> 描述**改造后**代码的位置一律以**工作树当前行号**为准，见 §4。两者不可混读。
> 快速对照：`publishRunEvent` main `:302-344` → 工作树 `:309-349`；`lastEventIDFrom` main `:211-225` → 工作树 `:215-229`；`AppendEvent` main `:145-172` → 工作树 `:177` 起；`buildMessageWithHistory` 的 `ListMessages` main `:66` → 工作树 `:69`。

### 2.1 后端已具备的能力（改造前即成立，本次基本没动）

| 能力 | 证据（文件:行号） |
| --- | --- |
| 执行与请求解耦：`context.WithoutCancel` | `service/conversation/run.go:67` |
| `StartChatRun` 非阻塞起 run、后台 goroutine 承载执行 | `service/conversation/run.go:58-78` |
| 进程内广播按 **run_id** 分键，Publish 永不阻塞 | `service/conversation/runbroker.go:21-26`、`:95-123`，缓冲 256 `:10` |
| 事件唯一出口：先落库拿 seq 再广播 | `service/conversation/run.go:302-344` |
| `AppendEvent` 单事务写事件 + 回填 `last_seq` | `adaptor/repo/run/run.go:145-172`（原文件位置） |
| 续传 `GET /api/chat/stream/run`，`Last-Event-ID` 优先 | `router/router.go:51` → `api/chat.go:80-125`、`:211-225` |
| 活跃查询 `GET /api/chat/run/active` | `router/router.go:52` → `api/chat.go:127-145` |
| `PumpRun` 先订阅再回放、按 seq 去重 | `service/conversation/run.go:133-196`（去重 `:182-184`） |
| 客户端断开时 `context.Canceled` 静默返回，不影响后台 | `api/chat.go:193-209` |

**结论**：`/api/chat/stream/run` 是**逐 run 的 GET 订阅**，前端同时持有 N 个 `run_id` 就能开 N 条 SSE。协议层零改动。

### 2.2 前端缺口（改造前）

| 层 | 缺口 | 位置 |
| --- | --- | --- |
| 状态（根因） | 单一 `isStreaming` / 单一 `controllerRef` / `isStreamingRef`，用页面级布尔表达会话级状态 | `App.tsx:239`、`:272`、`:277` |
| 闸门（11 处） | `loadSession` 直接 `if (isStreaming) return`——**禁止切会话**；`startNewSession`、`handleSubmit`、重挂 effect ×3、`loadOlderMessages`、`deleteSession`、`shareSession`、滚动加载同样被全局布尔拦 | `:555-557`、`:871-873`、`:909-913`、`:367-376`、`:1024-1027`、`:1052-1054`、`:758-761`、`:815-818`、`:844-847`、`:1347` |
| 生命周期 | 三条路径（提交/重挂/审批恢复）各自写同一组单例；**先结束者的 `finally` 清空全局状态**，会误判另一条仍在跑的会话 | `:941-944`/`:990-997`、`:1083-1085`/`:1114-1116`、`:1243`/`:1275-1276` |
| 串台（最危险） | `case 'session'` 与 `case 'done'` 无条件 `setSessionId(payload.session_id)`，并行时 A 的事件会把视图拽走 | `:1146-1151`、`:1200-1202` |
| 缓存失效 | `invalidateActiveSessionCache` 读 `sessionIdRef.current`，后台 run 结束时会清错会话 | `:751-756` |
| 停止 | `stopStreaming` 只能 abort 唯一那个 controller | `:1354-1356` |
| 侧栏 UI | 4 处 `disabled={isStreaming}`，其中 `:109` 就是"禁止切会话" | `SessionPanel.tsx:79/:109/:132/:146` |
| Composer | `if (!message || isStreaming) return` 全局闸门 | `Composer.tsx:32` |

### 2.3 后端缺失的约束（关键发现）

改造前后端**没有**同会话串行校验：`prepareChatRun` 直接 `runs.Create`，`GetActiveBySession` 仅用于查询接口。

而同会话并发确有害：`buildMessageWithHistory` 在 **run 启动时**一次性快照历史窗口（`service/conversation/chat.go:66`，limit=`historyWindowSize`=20，`helper.go:158`），第二条 run 看不到第一条尚未落库的产出 → 两条基于同一段历史并行生成，落库后上下文分叉。

**改造前之所以没出事，纯粹因为前端那道全局闸门碰巧保证了串行。** 放开闸门就必须把约束显式化，否则是拿副作用当契约。

---

## 3. 方案选型

四个候选，五维对比（实现成本 / 并发安全 / 内存 / 回归风险 / 与现有代码复用度）。

### 方案 A：放开 UI 闸门 + 靠现有 `reattachActiveRun` 补齐

删掉 `loadSession`/`startNewSession`/`handleSubmit` 的门禁与侧栏 `disabled`，切会话时 abort 当前流，切回靠重挂。

- **成本**：最低，约 30–60 行散点删改。
- **并发安全**：**差**。单例仍在：A 跑着切到 B，A 的 `finally`（`:991-992`）触发 `setIsStreaming(false)`，B 立刻被误判为空闲；B 提交后 `controllerRef` 被覆盖，A 的句柄丢失，`停止本轮` 会**停错会话**。
- **内存**：低（单连接）。
- **回归风险**：**高**。`setSessionId(payload.session_id)` 串台未处理，A 的事件会把视图拽回 A。
- **复用度**：中（复用 `reattachActiveRun`，但它假设"全局只有一条流"，两道门禁删了反而无保护）。
- **裁决：否决**。不满足 S1/S5/S6。

### 方案 B：会话维度 RunRegistry（**采纳**）

`Map<sessionId, RunHandle>` 取代单例布尔；切走 detach、切回按 `lastSeq` 用 `attachRun` 续接；`isBusy(sessionId)` 作同会话闸门。

- **成本**：中。前端 ~1000 行改动（`App.tsx` 780 行 diff）+ 3 个新文件；后端 ~100 行。
- **并发安全**：**好**。状态按会话分键，`finally` 只释放自己的句柄；无共享可变单例。
- **内存**：低。句柄只存标量（runId/lastSeq/assistantId/AbortController/状态位），上界 `MAX_ACTIVE_RUNS=8` 且有 LRU 淘汰。
- **回归风险**：中低。改动集中在 `App.tsx`；`pumpRun` 主循环与重试逻辑零改动；导出/分享/画像/成本不经手该状态。
- **复用度**：**最高**。`attachRun`、`readActiveRun`、`/api/chat/stream/run`、`/api/chat/run/active` 全部原样使用；现有"每事件写 `lastSeq`"正好是 S4 的游标来源。
- **裁决：采纳**。唯一同时满足 S1–S6 且不改协议的方案。

### 方案 C：多会话状态树全量分桶

把 `messages`/`messagePage`/`hasMoreMessages` 等全部提升为 `Map<sessionId, ViewState>`。

- **成本**：高（`App.tsx` 33 个 `useState` 中约 12 个需分桶，近全量重写）。
- **并发安全**：很好（隔离最彻底）。
- **内存**：中高（N 个会话各持完整消息数组，需重做 `SESSION_CACHE_MAX` 策略）。
- **回归风险**：高（滚动锚点、懒加载详情、导出、分享、画像、`assistantUpdateQueue` 全受影响）。
- **复用度**：中。
- **裁决：否决为过度设计**。并发只需要"会话级的**流式句柄**"，不需要"会话级的**整个视图**"。留作远期演进。

### 方案 D：后端多路复用（单连接订阅多 run）

新增 `GET /api/chat/stream/all` 或 WebSocket，一连接推送该用户全部活跃 run。

- **成本**：最高（新协议 + broker 多 run 聚合 + 前端分发器 + 多游标协商）。
- **并发安全**：好。
- **内存**：后端每连接维护多游标集合。
- **回归风险**：高（动 `api/`、`service/conversation`，直接冲击既有续传语义）。
- **复用度**：低（`PumpRun` 是单 run 语义，需重写为复用器）。
- **裁决：否决**。仅在并发会话数达数十、浏览器 6 连接上限成瓶颈时才值得；实测 1–3 条远未触及。

### 3.1 对比总表

| 维度 | A 放开闸门 | **B RunRegistry（采纳）** | C 状态树分桶 | D 多路复用 |
| --- | --- | --- | --- | --- |
| 实现成本 | 最低（~50 行） | **中（前端 ~1000 行 + 后端 ~100 行）** | 高（~800+ 行） | 最高（改协议） |
| S1 跨会话并行 | ✗ 单例互踩 | **✓** | ✓ | ✓ |
| S2 同会话串行 | ✗ 无约束 | **✓ 显式（前端 + 后端 409）** | ✓ | ✓ |
| S3 detach 不 abort | ✗ abort 即视为结束 | **✓** | ✓ | ✓ |
| S4 切回续接 | △ 易重复渲染 | **✓ lastSeq 单调续接** | ✓ | ✓ |
| S5 状态隔离 | ✗ 串台 | **✓** | ✓ | ✓ |
| S6 停止不误伤 | ✗ 停错会话 | **✓** | ✓ | ✓ |
| 内存 | 低 | **低（句柄标量 + LRU 8）** | 中高 | 中 |
| 回归风险 | 高 | **中低** | 高 | 高 |
| 复用度 | 中 | **最高** | 中 | 低 |
| 需改后端协议 | 否 | **否** | 否 | 是 |

---

## 4. 落地实现说明

> **行号基准声明**：本节（§4）所有行号指**改造后的工作树当前行号**（即交付状态），可直接对照现有文件。
> 与 §2 的"改造前 main 基线"不同基准，请勿混读。

### 4.1 改动文件清单

**新增（9，均为未跟踪文件）**

| 文件 | 作用 |
| --- | --- |
| `web/src/api/runRegistry.ts` | 会话维度句柄表（核心） |
| `web/src/chat/sessionStreamBuffer.ts` | 纯逻辑：投影/合并/去重，可零依赖单测 |
| `web/src/api/runRegistry.test.mjs` | 句柄表单测（12 PASS） |
| `web/src/api/parallelStream.test.mjs` | 端到端纯函数回归（切回/接管/落点，17 PASS） |
| `service/conversation/run_guard.go` | 后端进程内同会话守卫 + 配对约束说明 |
| `service/conversation/run_guard_test.go` | 守卫单测 |
| `api/chat_busy_data_test.go` | 409 响应体 `data.run_id` 组装测试 |
| `api/chat_errno_test.go` | errno 映射测试 |
| `api/chat_wiring_test.go` | handler 接线测试 |

**修改（前端）**：`web/src/App.tsx`（+780 行）、`web/src/api/runStream.ts`、`web/src/sessions/SessionPanel.tsx`、`web/src/composer/Composer.tsx`、`web/src/styles.css`、`web/src/api/runStream.test.mjs`（20 PASS）

**修改（后端）**：`service/conversation/run.go`、`service/conversation/chat.go`、`service/conversation/service.go`、`adaptor/repo/run/run.go`、`api/chat.go`、`api/handler.go`、`common/errno.go`（+ 对应测试）

**未改动**：`web/package.json`（新测试通过既有 test 脚本末尾动态 import 挂入）、`router/`、SSE 协议、DB schema（未加迁移）。

### 4.2 关键设计一：会话级 registry

`RunHandle` 每个会话独立持有 `runId / lastSeq / assistantId / controller / status / live / detached / superseded / updatedAt`。

三个容易踩的点，都在实现中显式处理了：

1. **`begin()` 对同会话二次登记抛错** —— 这是 S2 的**结构性**保证，不只靠 UI 判断。
2. **`superseded` 标记** —— 审批恢复复用同一条 run 时会重新登记句柄，旧句柄的 `finally` 会 flush + finish 掉刚登记的新句柄。置 `superseded` 后旧句柄 `finally` 直接 return。
3. **`rekey()` + `pendingSessionKeyRef`** —— 新会话首轮 `sessionId` 是空串，服务端建库后才回填。必须区分"本地调度键"（`pending:<uuid>`）与"发给服务端的 `session_id`"，否则 `begin('')` 抛错、首轮回复完全不出字。

### 4.3 关键设计二：detach vs abort

这是 S3 的核心，两者语义严格分开：

| | detach | abort |
| --- | --- | --- |
| 触发 | 切走会话（`registry.markDetached`） | 用户点「停止」/ 登出 / LRU 淘汰 |
| SSE 连接 | **保活**，继续接收增量 | 断开 |
| 后台执行 | 不受影响 | 不受影响（`WithoutCancel` 保证） |
| 视口 | 停止更新 | — |
| `lastSeq` | 持续推进（写入 live 数组） | 保留最后值 |

实现上，后台会话的增量**真身落在 live 数组**，flush 只做"视口投影"（`isViewing` 才入队），而不是把增量丢掉或直接改视口。这样切回时 `mergeLiveMessages` 一次性并回，文本只应用一次。

### 4.4 关键设计三：afterSeq 续接

切回时从 `handle.lastSeq` 起 `attachRun({ afterSeq })`（`App.tsx:1439-1442`）。服务端 `PumpRun` 先订阅再回放、并按 `seq <= cursor.seq` 去重，因此"切走期间漏掉的事件"靠回放补齐、"重复事件"靠 seq 过滤。`interrupted` 的 run 特殊处理：从 0 回放但只重投 `interrupt/done/error`，正文沿用历史里已有那条（避免整段重复渲染）。

### 4.5 关键设计四：同会话串行锁（前端 + 后端双闸）

**前端**（`App.tsx:1186-1189`）：提交前 `if (registry.isBusy(ownerSessionId))` 提示并拒绝。判定的是**这一个会话**，其他会话在跑不影响。

**后端**（`service/conversation/run_guard.go` + `run.go:365-385`）：`beginSessionRun` 抢占会话执行权，失败返回 `SessionBusyError` → `common.ErrSessionBusy` → HTTP **409**（`common.SessionBusy`），响应体带 `data.run_id`。

几个刻意的决定：

- **闸门位置在读历史窗口之前**。历史快照就是互踩点，放在 `Create` 之前已经晚了。
- **锁只拦 `running`，不拦 `interrupted`**。新增 `GetRunningBySession` 专用查询，与"可续传"语义的 `GetActiveBySession` 分开——混用会打断 `/api/chat/resume`。
- **defer 顺序**：先注册 `endSessionRun`、后注册 `recoverRun`，使实际执行顺序为 `recoverRun` → `endSessionRun`（LIFO）。反过来会出现"会话已被下一个请求占用、而上一条 run 还没标终态"的窗口。
- **失败路径必须归还**：`prepareChatRun`/`prepareResumeRun` 用具名返回值 + defer 判断 `err != nil`，而不是在每个 return 前手写释放（手写必漏）。
- **仓储只读兜底**：进程重启遗留的 `running` 行内存表看不见，再用 `GetRunningBySession` 查一次；查询失败放行（一次 DB 抖动不该把会话永久锁死）。
- **为什么用内存锁而非 DB 唯一约束**：本服务并发模型就是进程内的（goroutine + 进程内 broker + 单实例），内存守卫已覆盖全部执行者；部分唯一索引在 MySQL/测试用 sqlite 上支持不一致，还要引入迁移。

前端遇到 409 不是简单报错，而是 `adoptBusyRun` **挂到那条已有 run 上继续接收**（读 `data.run_id`，拿不到则回退 `GET /api/chat/run/active`），并移除本次提交的占位消息、把落点改指该 run 原有的 assistant 消息。

---

## 5. 验证证据

全部为本次**实际执行**的命令与结果（非引述）。

### 5.1 前端

```
$ cd web && npm test
...
PASS T6 超过上限时淘汰最久未活动的会话并 abort 其连接
PASS rekey 把待定键的句柄搬到真正的 session_id 上
PASS finish 只释放自己的句柄
PASS takeover 接管后旧句柄被标记 superseded，且释放它不影响新句柄
PASS 后台会话的 delta 只落在它自己那条消息上，当前会话不受污染
PASS 两个会话同时在跑时，各自的增量互不影响
PASS 切回时用 live 覆盖同 id 消息，不产生重复条目
PASS 后台会话收 2 条 delta 后切回，文本只应用一次（不翻倍）
PASS 409 接管后 delta 落在历史消息 hist-a1，不会被丢弃
PASS A2 边界：找不到既有 assistant 时，落点必须仍是可写消息而不是已删除的占位
...
runRegistry tests passed
parallelStream tests passed
→ 49 例全部 PASS，exit 0，FAIL 计数 0
→ 统计口径：cd web && npm run test 2>&1 | grep -c '^PASS'   # 输出 49

    分段拆解：runStream 20 + runRegistry 12 + parallelStream 17 = 49
    ⚠ profile sync 套件只打印汇总行、不产 PASS 行，因此**不能**用"各套件 PASS 累加"来核对总数。

$ npx tsc -b
（无输出 / 0 字节）  exit 0

$ npx vite build
✓ built in 13.56s   exit 0
主 chunk：dist/assets/index-C9xsr8_g.js  462.27 kB │ gzip: 144.99 kB
```

### 5.2 后端

```
$ go build ./...      → exit 0
$ go vet ./...        → exit 0
$ go test ./service/conversation/... ./adaptor/repo/run/... ./api/... -count=1
ok  edu.agent.code/service/conversation  0.852s
ok  edu.agent.code/adaptor/repo/run      0.349s
ok  edu.agent.code/api                   0.038s
→ exit 0
```

### 5.3 测试覆盖的关键行为

| 行为 | 断言要点 |
| --- | --- |
| 后台不污染视口 | 后台会话 delta 只落在自己那条消息；即便把后台缓冲区误套到视口，按**消息 id** 过滤也不串内容 |
| 切换不翻倍 | 后台收 2 条 delta 后切回，文本只应用一次；反向对照（不清投影队列会翻倍）已验证 |
| 同会话串行 | `begin()` 二次登记抛错 |
| 句柄不互相删除 | `finish` 只释放自己；`takeover` 后旧句柄释放不影响新句柄 |
| 408/409 接管落点 | 接管后 delta 落在历史消息而非已删占位；找不到既有 assistant 时落点仍可写 |
| 409 判定收紧 | 非会话忙的 409（如邮箱已注册）不按 busy 处理，且不触发 active run 查询；replay 入口 409 不触发定位 |

**如何证明 `run_guard.go` 本次只改注释不改逻辑（可选证据）**

`run_guard.go` 共 219 行、9 个函数签名。verifier 存档了「剥离注释后 AST 的 sha256」= `4d8ee1e7ce702ab5261328f24ee107c1216ee8975ec67aaf9252fbde549ec9e1`（219→114 行，9 个函数签名完整），可作佐证。

> **口径提示**：报告作者用 `go/parser` + `go/printer` 复算，得到 `1b76e3cd…`（137 行 / 9 函数）——与 verifier 的 `114 行` 不同，因**剥离注释与空行的归一化方式不同**（是否折叠空行、是否保留 decl 间分隔）。两者都指向「9 个函数签名完整」，但**哈希值不可跨工具比对**，请以 verifier 存档值 + 其复原方法为准。本节结论不依赖该哈希：新增文件本就无旧版本可 diff，逻辑正确性由 `run_guard_test.go` 的行为断言保证。

> **证据来源声明（重要 —— 请勿把以下证据归于报告作者）**
>
> 变异证据为**双向**变异，由 **verifier 独立执行并做逐字节还原校验**：
>
> - **变异 A**（`resolveTargetMessageId` 恒返回 fallback，模拟原缺陷）→ exit 1，PASS **49→42**，失败断言 `落点必须取 handle.assistantId，而不是闭包常量`（actual `'new-a1'` vs expected `'hist-a1'`）
> - **变异 B**（丢弃 fallback）→ exit 1，PASS **→42**，失败断言 `句柄为空时兜底用闭包常量（兼容路径）`
> - **还原后**：`sha256sum` = `1f39c256…`，`cmp` 逐字节 **CMP_IDENTICAL=YES**，复跑恢复 49 绿
>
> 分工说明：**变异 A/B 由 verifier 独立执行并逐字节还原校验**；**fe-dev（t14）与 reviewer（t15）各自独立产出过同类变异证据**；**报告作者未重跑上述变异，仅作引用**。
>
> 报告作者本人实际执行并负责的，仅 §5.1 与 §5.2 列出的命令（web `npm test` / `tsc -b` / `vite build`，go `build` / `vet` / `test`）。两类证据请勿混为一谈。

### 5.4 F8 已修复的硬证据（调用点 + 零残留）

`resolveTargetMessageId` 在 `App.tsx` 共 **4 处调用**，覆盖全部事件落点：

| 调用点 | 覆盖范围 |
| --- | --- |
| `:1266` | try 收尾 |
| **`:1296`** | **catch 收尾（本轮关键修复点）** |
| `:1330` | 覆盖 `:1332` / `:1344` |
| `:1522` | 覆盖 `:1547` tool_call / `:1555` tool_result / `:1574` progress / `:1578` delta / `:1592` / `:1602` |

**残留的闭包常量直调 `patchAssistant` = 0 处**。grep 命中的 8 处均位于各自函数 `const assistantId = ...` 声明之后的同一作用域内，属**变量遮蔽**而非残留。

一句话结论：**F8 的失败模式已根除，且由「4 个调用点 + 零残留」的 grep 证据锁定。**

---

## 6. 已知限制与后续建议

| # | 限制 | 影响 | 建议 |
| --- | --- | --- | --- |
| L1 | **同会话串行锁是进程内的**（`sessionRunGuard.running` map） | 多实例部署时跨实例无法互斥，同一会话可能被两个实例同时执行 | 优先**粘性会话**（按 user/session 哈希路由）；否则把锁外移 Redis `SET NX PX` + 续期，或加 DB 部分唯一索引 |
| L2 | **broker 是进程内的**（`runbroker.go`） | 多实例 + 负载均衡时，订阅者可能连到"没在跑这条 run"的实例，实时事件收不到 | 事件已落库，靠回放能自愈（`PumpRun` 会先回放再跟随），但会退化为轮询式体验；根治用 Redis Pub/Sub 或 NATS 广播 |
| L3 | 浏览器同域 SSE 上限 ~6 | 并发会话数高时连接被挤占 | 已设 `MAX_ACTIVE_RUNS=8` + LRU 淘汰（淘汰时 abort 其连接，靠 `lastSeq` 回放恢复）；若真需要更多，走方案 D |
| L4 | 多标签页同会话仍可能并发 | 各标签页 `sessionStorage` 独立，前端闸门管不到 | 已由后端 409 兜底；前端收到 409 会 `adoptBusyRun` 挂到已有 run |
| L5 | `ChatCompletion`（阻塞式）panic 时锁释放但库里 run 停在 `running` | 该会话会被 `GetRunningBySession` 持续拦截直到重启 | 已有的不对称行为，已在 `run_guard.go` 顶部注释登记；修复需单独评估补一条 defer 把 run 标 `failed` |
| L6 | 串行锁表无容量上限、无定时清理 | 正常路径全靠 defer 释放；**漏配 defer 会让该会话永久 409**，唯一自愈是重启 | 已在 `run_guard.go` 文件顶部写明强制配对约束与正确 defer 顺序；建议后续加"锁表年龄超阈值打 WARN"的诊断日志 |
| L7 | 本次无真实浏览器联调 | 视觉/手势/弱网行为未实测 | 按契约不做；上线前建议补一轮手工验收（见 §7） |
| L8 | **`tool_call` / `tool_result` 分支无直接落点断言**（grep 测试文件零命中） | **这是缺断言，不是缺修复**：这两个分支用的是同一个已覆盖的 `assistantId` 变量（由 `:1522` 统一解析），逻辑等价，当前行为正确。风险在于**未来**——若有人单独改动 tool 分支的落点逻辑，现有测试不会复红 | 建议后续为 tool 分支补一条直接断言，把"逻辑等价"锁成"测试可见" |

---

## 7. 上线路径建议（本次未部署）

### 7.1 前置条件

1. **代码合入**：`web/` 与后端改动需一起发布。前端新代码依赖后端 409 语义——若只发前端，同会话并发会退化为无保护（但仍不会崩，因为前端 `isBusy` 是第一道闸）。若只发后端，前端仍是旧单例逻辑，功能不生效但无回归。
   → **建议同时发布**，或先发后端（后端 409 对旧前端是兼容的新增错误码，旧前端会把它当普通错误显示）。
2. **无 DB 迁移**：本次未加表/字段，`GetRunningBySession` 只新增查询。上线不需要 ddl。
3. **`docs/` 被 `.gitignore:24` 忽略**，本报告与需求文档默认不进版本库；若需归档须显式 `git add -f` 或调整 ignore。

### 7.2 发布步骤

| 步骤 | 动作 | 验证 |
| --- | --- | --- |
| 1 | 构建后端二进制 | `go build ./... && go vet ./...` 通过 |
| 2 | 构建前端产物 | `cd web && npm test && npx tsc -b && npx vite build`，产物在 `web/dist/` |
| 3 | 灰度（建议单实例先上） | 观察：`SessionBusy` 错误率、`beginSessionRun: GetRunningBySession failed` 日志、锁泄漏（同会话持续 409 且无 run 在跑） |
| 4 | 手工验收 4 条主路径 | ① A 生成中切 B 再发一条 → B 正常流式；② A 生成中切回 A → 继续出字不重复；③ A 生成中向 A 再发 → 被提示拒绝；④ A 生成中点停止 → 只停 A，B 不受影响 |
| 5 | 观察 1–3 天 | 重点看 L6（锁泄漏导致的永久 409）与 L1/L2（若已多实例） |

### 7.3 回滚

无 schema 变更，回滚 = 回退二进制与静态资源。注意：**回滚后已在跑的 run 不受影响**（事件已落库，旧前端仍能用 `runStream.ts` 的续传逻辑挂载）。

### 7.4 若将来要多实例

按 L1/L2 处理，**必须**在扩容前完成，否则串行约束与实时广播同时失效。二者建议一起做（都用 Redis）：锁用 `SET NX PX`、广播用 Pub/Sub，可保持现有 `PumpRun`/`runbroker` 接口形状不变，只换实现。

---

## 8. 附：一句话总结

**后端的 run 维度基础设施早就够了，前端却用页面级布尔把并行锁死；把状态键从"页面"换成"会话"（detach 而非 abort、lastSeq 续接、isBusy 串行），再给后端补一道进程内同会话锁与 409，就同时拿到了跨会话并行与同会话串行——代价是这套锁与广播都还是进程内的，多实例之前必须外移。**
