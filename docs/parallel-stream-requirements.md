# 跨会话并行流式输出：需求与方案选型

- 任务：t1（requirements）／attempt 1
- 目标站点：analyse.kuangzihao.site
- Worktree：`/root/workspace/worktrees/analyse-parallel-stream`（基线 `10f6bc0 chore(release): 版本线升级到 release-2.5`）
- 本文档只做只读调研与方案设计，**不涉及任何业务代码改动**；落地实现由后续任务在 `web/src/` 内完成。

---

## 0. 结论摘要

1. **后端已经具备"跨会话并行"的全部必要条件**：run 是执行单元、事件落库可回放、broker 按 `run_id` 广播、续传接口按 `run_id` 挂载。后端没有任何"同时只能跑一条"的限制，而是一条 run 一个 goroutine（`service/conversation/run.go:68-76`）。
2. **真正阻塞并发的是前端**：`App.tsx` 用**单一** `isStreaming` + **单一** `controllerRef` 表达"整页在流式"，并在 6 处入口以它为闸门（其中 `loadSession` 的 `if (isStreaming) return` 直接禁止了"边生成边切会话"）。
3. **"同会话串行"目前只靠前端那道闸门碰巧成立**。后端 `prepareChatRun` 不做同会话活跃 run 校验（`service/conversation/run.go:345-406`），一旦前端放开闸门，同会话并发提交会互踩历史窗口快照。**因此串行约束必须显式补上**，不能依赖副作用。
4. **推荐方案 B：会话维度的 RunRegistry（句柄表）**。把"单例流式状态"换成 `Map<sessionId, RunHandle>`，切走 = detach（不 abort），切回 = 用 `attachRun` 从 `lastSeq` 继续接收；同会话有活跃句柄时拒绝新提交。该方案复用现有 `attachRun` / `readActiveRun` / `/api/chat/stream/run` 的比例最高，改动集中在 `App.tsx` 与 `SessionPanel.tsx`，无后端协议变更。
5. 验收口径：**自动化测试 + 构建通过 + 代码走查**，不做真实浏览器联调（见 §7）。

---

## 1. 现状盘点：后端已具备的能力

### 1.1 执行与请求解耦（`context.WithoutCancel`）

| 能力 | 证据（文件:行号） |
| --- | --- |
| `StartChatRun` 非阻塞起 run：请求 ctx 上完成准备 → 落 `chat_runs(running)` → 立刻返回 `runID/sessionID` | `service/conversation/run.go:58-78` |
| **客户端断开与后台执行解耦**：`bgCtx := context.WithoutCancel(runCtx)` | `service/conversation/run.go:67` |
| 后台 goroutine 承载执行，panic 兜底 | `service/conversation/run.go:68-76`（`defer s.recoverRun` @ `:69`） |
| 审批恢复走同一模式 | `service/conversation/run.go:85-101`，`WithoutCancel` @ `:90` |
| `recoverRun` 兜 panic，避免 run 永久停在 running | `service/conversation/run.go:736-758`（`WithoutCancel` @ `:748`） |
| Handler 只做"建 run + 转发事件"，断开只影响转发 | `api/chat.go:34-78`（注释 @ `:36-38`，`StartChatRun` @ `:51`） |

**含义**：一条 run 的生命周期与 HTTP 连接完全解耦。前端断开（切会话、刷新、切网）**不会**中断生成。这是"切走不 abort"能成立的底层前提。

### 1.2 进程内广播（runbroker）

| 能力 | 证据 |
| --- | --- |
| broker 只负责"正在看这条 run 的客户端"的实时推送，**投递不保证**；真身在库 | `service/conversation/runbroker.go:17-26` |
| 单订阅者缓冲 256 | `service/conversation/runbroker.go:8-10` |
| topic 按 **`run_id`** 分键（天然支持多 run 并存） | `service/conversation/runbroker.go:21-26`、`topicLocked` @ `:49-56` |
| `Subscribe` 返回 `(<-chan, cancel)`；topic 已关闭则返回已关闭 channel | `service/conversation/runbroker.go:58-93` |
| `Publish` 永不阻塞；缓冲满即关闭该订阅者，让它走"重连+回放" | `service/conversation/runbroker.go:95-123`（`:115-122`） |
| `Close` 关闭 topic 并使后续订阅立刻返回 | `service/conversation/runbroker.go:125-144` |
| 关闭 topic 保留上限 256，按插入序淘汰 | `service/conversation/runbroker.go:12-15`、`:146-154` |
| run 终态时关闭 topic | `service/conversation/run.go:727-729` |

**含义**：broker 是 `run_id` 维度的并发安全结构（`sync.Mutex` @ `:22`）。**多个会话同时有 run 在跑，broker 层零改动即可支撑**。

### 1.3 事件落库（`chat_run_events`）

| 能力 | 证据 |
| --- | --- |
| 表职责：`chat_runs` 记生命周期，`chat_run_events` 记每条可回放事件，`id` 即 SSE 游标 | `adaptor/repo/run/run.go:14-18` |
| 模型定义 | `adaptor/repo/model/run.go:5-28`（ChatRun）、`:31-44`（ChatRunEvent） |
| `Seq` = `chat_run_events.id`，全局自增 | `service/do/run.go:51` |
| `AppendEvent` 单事务内写事件 + 回填 `chat_runs.last_seq` | `adaptor/repo/run/run.go:145-172`（事务 @ `:152`，`seq = row.ID` @ `:164`，回填 @ `:165-167`） |
| `ListEventsAfter(runID, afterSeq, limit)` | `adaptor/repo/run/run.go:175`；接口声明 `adaptor/repo/run/run.go:30` |
| **事件唯一出口**：先落库拿 seq，再广播；返回值只反映落库成功，与客户端在线无关 | `service/conversation/run.go:302-344`（注释 @ `:302-306`，`AppendEvent` @ `:325`，仅 `seq>0` 才 `Publish` @ `:338-339`） |
| 分页回放页大小 500 | `service/conversation/run.go:205`、`:208-241` |

**含义**：事件是**按 run 持久化**的，任何时刻都能对任意 run 做"从 seq N 开始回放"。这正是"切回会话继续接收"的实现基础。

### 1.4 续传与活跃查询接口

| 能力 | 证据 |
| --- | --- |
| `GET /api/chat/stream/run` 断连续传（逐 run 订阅） | `router/router.go:51` → `api/chat.go:80-125` |
| 游标来源：`Last-Event-ID` 头优先，`?after=` 兜底 | `api/chat.go:211-225`（头 @ `:213`，query @ `:215`） |
| `run_id` 必填 | `api/chat.go:88-92` |
| **归属校验**：非本人 run 表现为"不存在" | `api/chat.go:95-104` → `service/conversation/run.go:115-131`（`:127`） |
| `GET /api/chat/run/active?session_id=` 查该会话活跃 run（running/interrupted） | `router/router.go:52` → `api/chat.go:127-145` → `service/conversation/run.go:103-113` |
| 仓储查询：`user_id + session_id + status IN (running, interrupted)`，`started_at DESC` 取一条 | `adaptor/repo/run/run.go:107-124`（`Where` @ `:113`，`Order` @ `:115`） |
| `PumpRun`：**先订阅再回放**（防丢失），按 seq 去重，跟随实时事件至终态 | `service/conversation/run.go:133-196`（顺序说明 @ `:136-137`，`Subscribe` @ `:148`，回放 @ `:152`，`ctx.Done()` @ `:171`） |
| 回放一个已结束的 run：补合成 error，避免客户端干等 | `service/conversation/run.go:243-268`、文案 @ `:271` |
| 终止事件定义：仅 `done` / `error`；**`interrupt` 是暂停不是终止** | `service/conversation/run.go:273-284` |
| 客户端断开时 `context.Canceled` 静默返回，不影响后台 run | `api/chat.go:193-209`（判断 @ `:199`） |
| 对外的 run 摘要 DTO | `service/dto/chat.go:179-200`（`ChatRunInfo` @ `:180`，`ChatRunActiveResp.Run` @ `:199`） |

**含义**：`/api/chat/stream/run` 是**逐 run 的 GET 订阅**，不存在"一个连接对应一个会话"的绑定。前端只要同时持有 N 个 `run_id`，就能开 N 条独立 SSE —— 接口层**无需任何改动**。

### 1.5 关键缺口：后端没有"同会话串行"约束

| 事实 | 证据 |
| --- | --- |
| `prepareChatRun` 直接 `runs.Create`，**未**检查该会话是否已有活跃 run | `service/conversation/run.go:345-406`（检查缺失；`Create` @ `:400-403`） |
| `GetActiveBySession` 只被 `GetActiveRun`（查询接口）使用，**没有**被创建路径用于拦截 | 全仓引用仅 `service/conversation/run.go:108` |
| 服务层无 per-session 互斥锁（唯一的 `sync.Mutex` 在 broker 内部） | `service/conversation/runbroker.go:22`；`service/conversation/chat.go` 无锁 |
| 历史窗口在 **run 启动时**一次性快照进 prompt（`ListMessages` limit=20） | `service/conversation/chat.go:60-103`（`ListMessages` @ `:66`，`historyWindowSize` = 20 @ `service/conversation/helper.go:158`） |
| 两条 run 各自 `finishRun` 写自己的 `run_id`，互不覆盖 | `service/conversation/run.go:702-732` |

**风险结论**：同会话并发提交时，第二条 run 的历史快照**看不到**第一条的产出（第一条尚未落库为消息），于是两条 run 基于同一段历史并行生成，落库后消息交错、上下文分叉。这解释了为什么"同会话串行"必须是**显式约束**，而不是靠"前端本来就这样"。

> 现状之所以成立：`App.tsx` 的全局 `isStreaming` 恰好让同会话不可能并发。放开并发闸门时，**必须同步把串行约束显式化**（前端拦截 + 后端兜底，见 §6.4）。

---

## 2. 前端缺口：全部阻塞并发的代码点

按层列出。**标注 `★` 的是一放开闸门就会立刻出错的位置**（不只是"被阻塞"，而是语义错误）。

### 2.1 API 层（`web/src/api/`）——基本可直接复用，缺"会话维度句柄"

| 位置 | 现状 | 判定 |
| --- | --- | --- |
| `web/src/api/runStream.ts:156-173` | `attachRun({runId, sessionId, afterSeq, signal, ...})`：按 `run_id` 挂载并跟随 | **可直接复用**，已是 run 维度 |
| `web/src/api/runStream.ts:84-116` | `streamChatRun`：POST 建 run + 自动续传 | 复用，但内部 state 每次新建（`:92-97`） |
| `web/src/api/runStream.ts:120-152` | `resumeChatRun`：审批恢复 | 复用 |
| `web/src/api/runStream.ts:176-239` | `pumpRun` 唯一接收循环，`state` 为**每次调用私有**（`:177`） | **天然可多路并发**，无全局单例 |
| `web/src/api/runStream.ts:200-208` | 收到终态才 `clearActiveRun(state.sessionId)` | 复用；见下方 ★ |
| `web/src/api/runStream.ts:36` | `ACTIVE_RUN_KEY_PREFIX = 'run:active:'` + **按 sessionId 分键** | 已支持多会话并存 |
| `web/src/api/runStream.ts:42-81` | `readActiveRun` / `writeActiveRun` / `clearActiveRun` 均按 session 分键 | 复用 |
| `web/src/api/runStream.ts:285-295` | `fetchRunEvents(runId, afterSeq)` → `GET /api/chat/stream/run`，双游标 | 复用 |
| `web/src/api/runStream.ts:297-318` | `discoverActiveRun(sessionId)` → `GET /api/chat/run/active` | 复用 |
| `web/src/api/sse.ts:9-27` | `consumeSSE` 无共享状态，纯流式解析 | 天然可多路 |
| `web/src/api/client.ts:28-35` | `fetchAuthorized` 无共享状态（仅 401 清 session） | 天然可多路 |
| ★ `web/src/api/runStream.ts:205, 267` | 终态时 `clearActiveRun(state.sessionId)` —— 若 `state.sessionId` 被后续事件改写（见 `:251-253`），可能清错会话 | 需改为**只用创建时锁定的 sessionId** |

**API 层结论**：`runStream.ts` **不需要新增文件能力**，只需（a）`attachRun`/`streamChatRun` 增加可选的 `onOpen(runId, sessionId)` 回调或返回值以登记句柄；（b）禁止用事件里的 `session_id` 覆写本地键（或至少不用于 `clearActiveRun`）。

### 2.2 App 状态层（`web/src/App.tsx`）——**根因所在**

**A. 单例状态定义（根因）**

| 行号 | 代码 | 问题 |
| --- | --- | --- |
| `App.tsx:239` | `const [isStreaming, setIsStreaming] = useState(false)` | **全局布尔**，表达"整页在流式"，无法区分会话 |
| `App.tsx:272` | `const controllerRef = useRef<AbortController \| null>(null)` | **单一** controller，第二条流会覆盖第一条的句柄 |
| `App.tsx:277` | `const isStreamingRef = useRef(false)` | 单例镜像 |
| `App.tsx:360-362` | `useEffect(() => { isStreamingRef.current = isStreaming }, [isStreaming])` | 单例同步 |
| `App.tsx:300` | `const reattachedSessionsRef = useRef<Set<string>>(new Set())` | 每会话只重挂一次，并行场景下语义需重审 |

**B. 以 `isStreaming` 为闸门、禁止并发的入口（6 处硬闸 + 3 处软闸）**

| 行号 | 代码 | 影响 |
| --- | --- | --- |
| ★ `App.tsx:555-557` | `loadSession()` 首行 `if (isStreaming) { return; }` | **直接禁止"生成中切会话"** —— 核心阻塞点 |
| ★ `App.tsx:871-873` | `startNewSession()` 首行 `if (isStreaming) { return; }` | 禁止"生成中新建会话" |
| ★ `App.tsx:909-913` | `handleSubmit`：`if (!message \|\| isStreamingRef.current) return` | 全局锁：任何会话在跑就拒绝一切提交（含别的会话） |
| ★ `App.tsx:367-376` | 重挂 effect：`if (!sessionId \|\| isLoadingSessionDetail \|\| isStreaming) return` | 全局在跑时不重挂当前会话 |
| ★ `App.tsx:1024-1027` | `reattachActiveRun` 首行 `if (!targetSessionId \|\| isStreamingRef.current) return` | 同上 |
| ★ `App.tsx:1052-1054` | `reattachActiveRun` 二次检查 `if (isStreamingRef.current) return` | 竞态兜底，同样以全局为条件 |
| `App.tsx:758-761` | `loadOlderMessages`：`if (isStreaming \|\| ...) return` | 其他会话在跑时无法加载历史 |
| `App.tsx:815-818` | `deleteSession`：`if (isStreaming) return` | 同上（且删除正在跑的会话需专门处理） |
| `App.tsx:844-847` | `shareSession`：`if (isStreaming) return` | 同上 |
| `App.tsx:1347` | `handleConversationScroll`：`!isStreamingRef.current` 才触发加载更早 | 同上 |

**C. 流式生命周期的单例写入（3 条路径）**

| 行号 | 代码 | 问题 |
| --- | --- | --- |
| `App.tsx:941-944` | `setIsStreaming(true)`；`new AbortController()`；`controllerRef.current = controller` | 提交路径独占单例 |
| `App.tsx:990-997` | `finally { controllerRef.current = null; setIsStreaming(false); ... }` | **先结束者清空全局状态**，导致另一条仍在跑的会话被误判为"已结束" |
| `App.tsx:1083-1085` | `reattachActiveRun` 同样写单例 | 与提交路径互相覆盖 |
| `App.tsx:1114-1116` | 同上 finally 清空单例 | 同上 |
| `App.tsx:1243` / `App.tsx:1275-1276` | `resumeInterruptedRun` 写/清单例 | 审批恢复路径同样互斥 |
| `App.tsx:1354-1356` | `stopStreaming = () => controllerRef.current?.abort()` | **只有一个 controller 可停**，且并行时可能停错会话 |

**D. 事件回调里的跨会话串台隐患（最危险）**

| 行号 | 代码 | 后果 |
| --- | --- | --- |
| ★ `App.tsx:1146-1151` | `handleStreamEvent` 的 `case 'session': if (payload.session_id) setSessionId(payload.session_id)` | 并行时 A 会话的事件会把视图**强行切到 A**，用户正在看的 B 被打断 |
| ★ `App.tsx:1200-1202` | `case 'done': if (payload.result?.session_id) setSessionId(...)` | 同上 |
| ★ `App.tsx:751-756` | `invalidateActiveSessionCache()` 用 `sessionIdRef.current`（当前视图会话） | 后台 run 结束时清的是**当前查看的会话**缓存，真正的会话缓存没清 → 切回去看到残影 |
| `App.tsx:1284-1288` | `updateTrace` → `setLastTraceId`（全局） | 并行时顶部 trace 乱跳（展示问题，非阻塞） |
| `App.tsx:1304-1319` | `patchAssistant` / `scheduleAssistantUpdateFlush` 用单一 `assistantUpdateTimerRef`（`:298`） | 队列按 messageId 分桶（`:1305`），**不会串内容**，但多路共用 40ms 节流；可接受，需确认 |
| `App.tsx:1382-1386` | `activeSession` / `activeMessageIndex` / `activeHue` 基于当前 `messages` | 视图派生，切会话后自动正确；无需改 |

**App 层结论**：需要把 `isStreaming: boolean` + `controllerRef` 重构为 `Map<sessionId, RunHandle>`（推荐 B，见 §5），并把 `setSessionId(payload.session_id)` 改为"仅当事件所属 run 就是当前视图会话时才更新"。

### 2.3 侧栏 UI 层（`web/src/sessions/SessionPanel.tsx`）——4 处 `disabled={isStreaming}`

| 行号 | 控件 | 现状 |
| --- | --- | --- |
| `SessionPanel.tsx:18` / `:38` | props 解构与类型：`isStreaming: boolean` | 布尔入参，需改为"每会话是否在跑"的判定函数 |
| `SessionPanel.tsx:79` | `新建分析` 按钮 `disabled={isStreaming}` | 生成中不能新建 |
| `SessionPanel.tsx:109` | 会话项主按钮 `disabled={isStreaming}`（即**不能切换会话**） | 核心 UX 阻塞 |
| `SessionPanel.tsx:132` | 分享按钮 `disabled={isStreaming}` | 次要 |
| `SessionPanel.tsx:146` | 删除按钮 `disabled={isStreaming}` | 次要（但删除"正在跑的会话"需专门语义） |

### 2.4 Composer 层（`web/src/composer/Composer.tsx`）——单轮语义

| 行号 | 代码 | 问题 |
| --- | --- | --- |
| `Composer.tsx:7` / `:16` | props `isStreaming: boolean` | 全局布尔 |
| `Composer.tsx:32` | `if (!message \|\| isStreaming) return` | 本地提交闸门 |
| `Composer.tsx:61` | 提示文案 `'正在返回，可随时停止本轮。'` | 需按"当前会话"判定 |
| `Composer.tsx:87-94` | 同一按钮在 `isStreaming` 时变成 `停止本轮`，`onClick={onStop}` | **发送与停止复用同一按钮**是单流式的直接体现；并行后需明确：按钮只反映**当前会话**状态，其他会话在跑不影响本会话可发送 |

### 2.5 App → 子组件传参（需同步改造的调用点）

| 行号 | 传参 |
| --- | --- |
| `App.tsx:1397` | `<SessionPanel isStreaming={isStreaming} ...>` |
| `App.tsx:1472-1473` | 顶部状态章 `run-stamp ${isStreaming ? 'live' : 'ready'}` / `RUNNING` \| `READY` |
| `App.tsx:1507` | `<Composer isStreaming={isStreaming} ...>`，`onStop={stopStreaming}` @ `:1512` |

### 2.6 缺口汇总（一句话）

> 前端的并发能力不是"漏了一处判断"，而是**数据模型选错了**：用页面级布尔表达会话级状态。因此改造必须把状态键从"页面"换成"会话"，而不是逐处删掉 `if (isStreaming)`。

---

## 3. 目标语义（已与用户确认）

### 3.1 硬约束

| 编号 | 约束 | 定义 |
| --- | --- | --- |
| S1 | **跨会话并行** | 会话 A 的 run 在跑时，会话 B 可以独立发起新 run 并流式接收。两者 SSE 连接、事件序列、渲染互不干扰。 |
| S2 | **同会话串行** | 会话 A 已有活跃 run（running / interrupted）时，向 A 再发一条**必须被拒**（前端阻止提交并提示；后端兜底返回明确错误）。 |
| S3 | **切走是 detach，不是 abort** | 从 A 切到 B **不中止** A 的请求/执行。A 的 SSE 可以被主动关闭（释放连接），但后端 run 照常执行并落库。 |
| S4 | **切回继续接收** | 切回 A 时，从 A 的 `lastSeq` 起通过 `/api/chat/stream/run` 继续接收；表现等价于"没离开过"，事件不重不漏、不重复渲染。 |
| S5 | **状态隔离** | A 的流式事件不得改变用户当前正在查看的会话（禁止 `setSessionId` 串台）。 |
| S6 | **停止按钮语义** | `停止本轮` 只作用于**当前会话**的 run；不误停其他会话。 |

### 3.2 对 S3 的精确化（重要）

"detach 不 abort" 分两层，必须写清楚：

- **执行层**：绝不调用后端取消接口；后端 run 无论如何都跑到终态（已由 `context.WithoutCancel` 保证，`service/conversation/run.go:67`）。
- **传输层**：允许 `AbortController.abort()` 关闭 SSE 连接以省资源，**但**：
  - 必须在 abort 前把 `{runId, lastSeq}` 落到 `sessionStorage`（现有 `writeActiveRun` 已在每个事件后写，`web/src/api/runStream.ts:265-271`）；
  - abort 后**不得**把该会话标记为"已完成/失败"，也不得清空 `activeRun` 记录；
  - 切回时必须能从 `lastSeq` 续上（`attachRun`，`web/src/api/runStream.ts:156-173`）。

> 为降低首版复杂度，推荐首版**保留连接不 abort**（浏览器对同域 SSE 有 6 连接上限，实际并发 1–3 条无压力），仅当用户显式"停止"或登出时 abort。这消除"abort → 重连 → 重复事件"的一整类回归风险。若后续要限制连接数，再启用"超过 N 条则 abort 最久未查看的会话"，此时 S3/S4 仍由 `lastSeq` 回放保证。

### 3.3 不可变项（明确不做）

- 不引入 WebSocket；不改 `/api/chat/*` 协议与事件格式。
- 不改后端 broker 语义（它已经是 run 维度）。
- 不做"同一会话内多 run 并行"（明确违反 S2）。
- 不碰生产环境；只交付工作树 + 构建验证。

---

## 4. 方案选型对比

### 方案 A：最小改动 —— 放开 UI 闸门，靠 `reattachActiveRun` 补齐

**做法**：删掉 `App.tsx:555`、`:871`、`:911` 的门禁与 `SessionPanel` 的 `disabled`；切会话时 abort 当前流；切回时由现有 `reattachActiveRun` 重挂。

| 维度 | 评价 |
| --- | --- |
| 实现成本 | **最低**（约 30–60 行，散点删改） |
| 并发安全 | **差**。单例 `isStreaming`/`controllerRef` 仍在：A 跑着时切到 B，`setIsStreaming(false)` 由 A 的 finally 触发（`:991-992`），B 立刻被误判为空闲；B 提交后 `controllerRef` 被覆盖，A 的句柄丢失，`停止本轮` 会停错。 |
| 内存 | 低（单连接） |
| 回归风险 | **高**。`setSessionId(payload.session_id)` 串台（`:1146-1151`、`:1200-1202`）未处理，A 的事件会把视图拽回 A。 |
| 复用度 | 中（复用 `reattachActiveRun`，但它假设"全局只有一条流"，`:1025`、`:1052` 两道门禁要一起删，删了反而无保护） |
| 结论 | **不满足 S1/S5/S6**，仅适合做"是否可行"的探针，不作为交付方案。 |

### 方案 B：会话维度 RunRegistry（**推荐**）

**做法**：新增 `web/src/api/runRegistry.ts`（纯逻辑，可单测），把"单例流式状态"替换为句柄表：

```ts
type RunHandle = {
  sessionId: string;
  runId: string;
  lastSeq: number;
  assistantId: string;      // 该会话当前 assistant 消息 id
  status: 'running' | 'interrupted' | 'done' | 'error';
  controller?: AbortController;
};
class RunRegistry {
  get(sessionId): RunHandle | undefined
  isBusy(sessionId): boolean          // 同会话串行闸门（S2）
  upsert(handle): void
  setSeq(sessionId, seq): void
  release(sessionId): void
  busySessions(): string[]            // 驱动侧栏"生成中"圆点
}
```

- `App.tsx`：删除 `isStreaming`/`controllerRef`/`isStreamingRef`，改为 `const registryRef = useRef(new RunRegistry())` + 一个 `useSyncExternalStore`（或轻量 `version` state）触发重渲染；对外暴露 `isStreamingFor(sessionId)`。
- `loadSession`：**不再 return**；`detach` = 只切视图，不动 registry（首版不 abort）。
- 提交：`handleSubmit` 先 `if (registry.isBusy(sessionIdRef.current)) { 提示"该会话正在生成"; return; }` —— **S2 在前端落地**。
- 切回：`loadSession` 末尾按 `registry.get(sessionId)` 或 `/api/chat/run/active` 判断，命中则 `attachRun({ afterSeq: handle.lastSeq })` 继续接收 —— **S4 复用现有能力**。
- 事件：`handleStreamEvent` 增加 `ownerSessionId` 参数；`case 'session'` / `case 'done'` 的 `setSessionId` 改为"仅当 `ownerSessionId === sessionIdRef.current`"（S5）。
- 停止：`stopStreaming` 只 abort `registry.get(sessionIdRef.current)`（S6）。
- 侧栏：`SessionPanel` 的 `isStreaming: boolean` → `isBusy: (sessionId: string) => boolean`；`新建分析`（`:79`）不再 disabled，会话项（`:109`）不再 disabled，仅在会话条目上加"生成中"指示。
- Composer：`isStreaming` → `isStreamingFor(currentSession)`，只反映当前会话。

| 维度 | 评价 |
| --- | --- |
| 实现成本 | **中**（`runRegistry.ts` ~120 行 + `App.tsx` 改造 ~200 行 + 侧栏/Composer 各 ~20 行；无后端改动） |
| 并发安全 | **好**。状态按会话分键，`finally` 只释放**自己的** handle；无共享可变单例。 |
| 内存 | 低。Map 条目数 = 同时在跑的会话数（实测 1–3）；`release` 后删除，可加上限（如 8，超出按 LRU 释放最久未查看者并 abort 其连接）。每条 handle 只存标量，无消息体副本。 |
| 回归风险 | **中低**。改动集中在 `App.tsx` 单文件；`runStream.ts` 只加一个可选回调，不动 `pumpRun` 主循环；其余页面（导出、分享、画像、成本）不经手该状态。 |
| 复用度 | **最高**。`attachRun`（`runStream.ts:156`）、`readActiveRun`（`:42`）、`/api/chat/stream/run`、`/api/chat/run/active` 全部原样使用；`writeActiveRun` 的"每事件写 lastSeq"（`:265-271`）正好是 S4 的游标来源。 |
| 结论 | **推荐**。唯一方案能同时满足 S1–S6，且与现有后端协议零冲突。 |

### 方案 C：多会话状态树全量分桶

**做法**：把 `messages` / `messagePage` / `hasMoreMessages` / `isLoadingSessionDetail` / `sessionCacheRef` 全部提升为 `Map<sessionId, SessionViewState>`，用一个 `useReducer` 管理多会话视图。

| 维度 | 评价 |
| --- | --- |
| 实现成本 | **高**（`App.tsx` 1570 行几乎全量重写；33 个 `useState` 中约 12 个需分桶） |
| 并发安全 | **很好**（隔离最彻底） |
| 内存 | **中高**。N 个会话各自持有完整消息数组；现有 `SESSION_CACHE_MAX`（`App.tsx:684-689`）策略需重做。 |
| 回归风险 | **高**。滚动锚点（`:347-358`）、懒加载详情（`:695-740`）、导出（`:891-907`）、分享、画像、`assistantUpdateQueue`（`:297`）全部受影响。 |
| 复用度 | 中（API 层同样复用，但视图层几乎重写） |
| 结论 | 过度设计。并发只需要"会话级的**流式句柄**"，不需要"会话级的**整个视图**"。留作 B 后续若出现"多会话同时滚动/编辑"需求时的演进方向。 |

### 方案 D：后端多路复用（单连接订阅多 run）

**做法**：新增 `GET /api/chat/stream/all`（或 WebSocket），一个连接推送该用户全部活跃 run 的事件，前端按 `session_id` 分发。

| 维度 | 评价 |
| --- | --- |
| 实现成本 | **最高**（后端新协议 + broker 多 run 聚合 + 前端分发器 + 断线重连时多游标协商） |
| 并发安全 | 好（后端统一调度） |
| 内存 | 后端每连接需维护多游标集合 |
| 回归风险 | **高**。改动 `api/` 与 `service/conversation`，直接影响现有断续传语义；且违反本任务"only docs"边界。 |
| 复用度 | **低**。现有 `PumpRun` 是单 run 语义，需重写为多 run 复用器。 |
| 结论 | 不采纳。仅在"并发会话数达到数十"（浏览器连接数成为瓶颈）时才值得；当前 1–3 条 SSE 远未触及 6 连接上限。 |

### 4.1 对比总表

| 维度 | A 放开闸门 | **B RunRegistry** | C 状态树分桶 | D 多路复用 |
| --- | --- | --- | --- | --- |
| 实现成本 | 最低（~50 行） | **中（~340 行）** | 高（~800+ 行） | 最高（前后端协议） |
| 跨会话并行 S1 | ✗（单例互踩） | **✓** | ✓ | ✓ |
| 同会话串行 S2 | ✗（无约束） | **✓（显式）** | ✓ | ✓ |
| detach 不 abort S3 | ✗（abort 即视为结束） | **✓** | ✓ | ✓ |
| 切回继续接收 S4 | △（重连+回放，易重复） | **✓（lastSeq 续传）** | ✓ | ✓ |
| 状态隔离 S5 | ✗（`setSessionId` 串台） | **✓** | ✓ | ✓ |
| 停止按钮 S6 | ✗（停错会话） | **✓** | ✓ | ✓ |
| 内存 | 低 | **低（句柄标量）** | 中高（消息副本） | 中（后端游标集） |
| 回归风险 | 高 | **中低** | 高 | 高 |
| 与 `attachRun`/`readActiveRun` 复用度 | 中 | **最高（原样复用）** | 中 | 低 |
| 是否需要改后端 | 否 | **否** | 否 | 是 |
| 结论 | 否决 | **采纳** | 备选（远期） | 否决 |

---

## 5. 推荐方案 B 的详细设计要点

### 5.1 新增文件

`web/src/api/runRegistry.ts`（纯函数/类，无 DOM 依赖，可被 `node --experimental-strip-types` 直接单测）：

| 方法 | 语义 | 对应约束 |
| --- | --- | --- |
| `isBusy(sessionId)` | 该会话是否有 running/interrupted 句柄 | S2 闸门 |
| `begin(sessionId, {runId, assistantId, status})` | 登记/覆盖句柄；若已 busy 则**抛错**（防御编程，双保险） | S2 |
| `touch(sessionId, lastSeq)` | 更新游标（由 `onEvent` 调用） | S4 |
| `finish(sessionId, status)` | 置终态并 `delete`（或保留到 UI 消费后清理） | S3/S4 |
| `detach(sessionId)` | **仅**标记"用户已离开视图"，**不清句柄、不 abort** | S3 |
| `busySessions(): string[]` | 供侧栏渲染"生成中"指示 | S1 可见性 |

### 5.2 `App.tsx` 改造清单（实现任务用）

| 位置 | 改动 |
| --- | --- |
| `:239` `:272` `:277` `:360-362` | 删除单例；引入 `registryRef` + 订阅（`useSyncExternalStore` 或 `version` state） |
| `:555-557` | 删除 `if (isStreaming) return`；改为 `registry.detach(prevSessionId)` 后正常切换 |
| `:871-873` | 删除门禁（新建会话与任何在跑的 run 无关） |
| `:909-913` | 判定改为 `registry.isBusy(sessionIdRef.current)` |
| `:367-376` `:1024-1027` `:1052-1054` | 门禁条件从全局改为**该会话**是否已有句柄 |
| `:941-944` / `:990-997` | `registry.begin(...)` / `finally registry.finish(sessionId)` —— **只释放自己的** |
| `:1083-1085` / `:1114-1116` | 同上（重挂路径） |
| `:1243` / `:1275-1276` | 同上（审批恢复路径） |
| `:1146-1151` `:1200-1202` | `setSessionId` 加 `ownerSessionId === sessionIdRef.current` 守卫（S5） |
| `:751-756` | `invalidateActiveSessionCache(sessionId)` 显式传参，不再读 `sessionIdRef.current`（S5） |
| `:1354-1356` | `stopStreaming` 只 abort 当前会话句柄（S6） |
| `:758-761` `:815-818` `:844-847` `:1347` | 门禁改为会话级（其他会话在跑不阻挡本会话） |
| `:1397` `:1472-1473` `:1507` | 传参改为 `isBusy` / `isStreamingFor(current)` |

### 5.3 `runStream.ts` 最小改动

- 给 `streamChatRun` / `attachRun` 增加可选 `onRunStart?: (info: {runId, sessionId}) => void`（在首个 `ready` 事件后回调），让 App 能尽早登记句柄；
- `applyEvent` 里 `state.sessionId` 的赋值（`:251-253`）保留，但**禁止**用它做 `clearActiveRun` 的键；`clearActiveRun` 一律使用**创建时锁定**的 sessionId（修正 `:205`、`:267`）。

### 5.4 同会话串行的后端兜底（可选，建议纳入后续任务）

前端闸门存在多标签页/直接调 API 的绕过可能。建议后续在 `service/conversation/run.go:345` 的 `prepareChatRun` 开头加：

```
active, err := s.runs.GetActiveBySession(ctx, userID, session.SessionID)
if active != nil { return 409 / "该会话已有正在执行的对话" }
```

> 注意：需处理"点击重试/网络抖动导致的重复提交"与"interrupted run 待审批"两种既有场景，否则会误伤（`interrupted` 状态本身也满足 `GetActiveBySession` 的条件，见 `adaptor/repo/run/run.go:113-114`）。因此该兜底**必须**与前端“同会话先 resume 再新建”的语义一起设计，不应顺手加上。本任务只登记为后续项。

---

## 6. 边界与风险登记

| 编号 | 风险 | 处置 |
| --- | --- | --- |
| R1 | 浏览器同域 SSE 并发上限 6 | 首版不 abort，支持 ~5 会话并行足够；`RunRegistry` 预留 LRU 上限 8 |
| R2 | 多标签页同会话并发 | 前端闸门无法跨标签页；由 `sessionStorage` 各自独立（`runStream.ts:36`），需靠后端兜底（§5.4） |
| R3 | 切回时事件重复渲染 | 用 `lastSeq` 严格单调去重（后端 `PumpRun` 已按 `seq <= cursor.seq` 过滤，`run.go:182-184`）；前端 `attachRun({afterSeq: lastSeq})` |
| R4 | `interrupted` 语义 | `interrupt` 非终止事件（`run.go:273-284`），中断会话仍算 busy，切回走"只重投 interrupt/done/error"（现有 `App.tsx:1056-1064`） |
| R5 | 删除正在跑的会话 | 需先 `stop` 并清句柄；后端删除会话时 run 事件仍在写库，建议删除前弹确认并 abort 连接 |
| R6 | 登出时残留连接 | `handleLogout`（`App.tsx:492`）中 abort 全部句柄并清空 registry |
| R7 | `assistantUpdateTimerRef` 共用节流 | 队列按 messageId 分桶（`App.tsx:1305`），不会串内容；仅共享 40ms 节流窗口，可接受 |
| R8 | 内存增长 | 句柄只存标量（runId/lastSeq/assistantId/status）；消息仍在 `sessionCacheRef`（`:287`，已有 `SESSION_CACHE_MAX` 上限 `:684-689`） |

---

## 7. 验收口径

**不做真实浏览器联调**。验收 = 自动化测试 + 构建通过 + 代码走查三件套。

### 7.1 自动化测试（新增，纯 Node，无需浏览器）

沿用现有基建：`web/src/api/runStream.test.mjs` 已用 `registerHooks` 补 `.ts` 扩展名 + `window`/`sessionStorage` 替身（`web/src/api/runStream.test.mjs:1-40`）。

| 编号 | 用例（文件：`web/src/api/runRegistry.test.mjs`） | 断言 |
| --- | --- | --- |
| T1 | 两会话各自 `begin`，互不覆盖 | `registry.get('A').runId !== registry.get('B').runId`；`isBusy` 均为 true |
| T2 | A `finish` 后，B 仍 busy | `isBusy('A') === false && isBusy('B') === true`（**验证单例 bug 已消除**） |
| T3 | 同会话二次 `begin` 被拒 | 抛错 / `isBusy('A') === true` 且原句柄不变（S2） |
| T4 | `detach` 不清句柄、不 abort | `isBusy` 仍 true；`controller.abort` 未被调用（S3） |
| T5 | `touch` 单调推进游标，切回复用 | `attachRun` 收到的 `afterSeq === 最后一次 touch 值`（S4） |
| T6 | 句柄数超上限时按 LRU 释放并 abort | 最久未查看者被释放（R1） |

补充：扩展 `web/src/api/runStream.test.mjs` —— 两个并行 `pumpRun` 实例互不影响 `state`，且 `clearActiveRun` 只清自己的 session 键（覆盖 `:205`/`:267` 的键错风险）。

### 7.2 构建与既有测试

```bash
# 后端（无改动也应通过，作为基线）
cd /root/workspace/worktrees/analyse-parallel-stream
go build ./... && go vet ./...
go test ./service/conversation/... -count=1

# 前端类型检查 + 生产构建（tsc -b 会因类型不匹配直接失败）
cd /root/workspace/worktrees/analyse-parallel-stream/web
npm run build          # = tsc -b && vite build

# 既有 6 个 .test.mjs + 新增 runRegistry.test.mjs
npm test
```

> `npm test` 需把新文件串进 `package.json` 的 test 脚本（现有 6 条 `node --experimental-strip-types ...` 并列，见 `web/package.json:9`）。

### 7.3 代码走查（静态可验证，逐条 grep）

| 编号 | 检查点 | 判定命令/条件 |
| --- | --- | --- |
| C1 | 不再存在全局流式闸门 | `grep -n "isStreaming" web/src/App.tsx \| grep -E "useState\|useRef"` **必须无输出**（当前基线会命中 `:239` 与 `:277`） |
| C2 | `loadSession` 无并发闸门 | `sed -n '554,560p' web/src/App.tsx` 不含 `if (isStreaming)` |
| C3 | 侧栏不再整体禁用 | `grep -n "disabled={isStreaming}" web/src/sessions/SessionPanel.tsx` 无输出 |
| C4 | 停止只作用于当前会话 | `stopStreaming` 内引用 `registry.get(sessionIdRef.current)` 且无裸 `controllerRef.abort` |
| C5 | 不串台 | `grep -n "setSessionId(payload" web/src/App.tsx` 每处都在 owner 守卫内 |
| C6 | 切走不 abort | `loadSession` 内无 `abort()` 调用 |
| C7 | 同会话串行 | `handleSubmit` 首行含 `registry.isBusy(...)` 判定并给出用户可见提示 |
| C8 | 后端零改动 | `git -C /root/workspace/worktrees/analyse-parallel-stream diff --stat` 不含 `service/`、`api/`、`adaptor/` |

### 7.4 明确不在验收范围

- 真实浏览器多标签、弱网切换、移动端手势联调；
- 后端同会话串行的 409 兜底（登记为后续任务，见 §5.4）；
- 性能压测（>5 会话并行）。

---

## 8. 文件级交付路径（本任务 + 后续实现任务）

### 8.1 本任务（t1，已完成范围）

| 路径 | 说明 |
| --- | --- |
| `docs/parallel-stream-requirements.md` | 本文档（唯一交付物） |

验收命令：

```bash
test -s /root/workspace/worktrees/analyse-parallel-stream/docs/parallel-stream-requirements.md && echo OK
```

> **交付事实（需知悉）**：`.gitignore:24` 忽略了 `docs` 目录（标注"内部资料（不随仓库分发）"）。`docs/context_compaction.md`、`docs/context_compaction_eval.md` 虽存在于工作树，但**仅这两个是历史强制添加的已跟踪文件**；本文档为**未跟踪**状态。
> 这不影响本任务的验收口径（验收即"工作树内文件存在且非空"），但若后续需要把这份契约纳入版本库，必须显式 `git add -f docs/parallel-stream-requirements.md`，或由负责人调整 `.gitignore` —— **本任务不擅自改动 `.gitignore` 或执行强制添加**。

### 8.2 后续实现任务（建议新建 tâche，不在本任务范围）

| 路径 | 动作 | 预估 |
| --- | --- | --- |
| `web/src/api/runRegistry.ts` | **新增**：会话维度句柄表（§5.1） | ~120 行 |
| `web/src/api/runRegistry.test.mjs` | **新增**：T1–T6 | ~200 行 |
| `web/src/api/runStream.ts` | 改：加 `onRunStart`；`clearActiveRun` 用锁定 sessionId（§5.3） | ~20 行 |
| `web/src/App.tsx` | 改：单例 → registry，13 处门禁/生命周期/串台（§5.2） | ~200 行 |
| `web/src/sessions/SessionPanel.tsx` | 改：`isStreaming: boolean` → `isBusy(sessionId)`，去掉 4 处 disabled（`:18,:38,:79,:109,:132,:146`） | ~25 行 |
| `web/src/composer/Composer.tsx` | 改：`isStreaming` 语义收敛为"当前会话"（`:16,:32,:61,:87-94`） | ~15 行 |
| `web/package.json` | 改：`test` 脚本加入 `runRegistry.test.mjs` | 1 行 |
| `service/conversation/run.go` | **可选后续**：同会话 409 兜底（§5.4，需与 interrupted 语义一起设计） | ~15 行 |

> 除最后一行外，全部改动在 `web/`，不触碰 `service/`、`api/`、`adaptor/`，符合"只交付工作树 + 构建验证"的边界。

---

## 9. 附录：行号索引（按文件）

| 文件 | 关键行号 |
| --- | --- |
| `service/conversation/run.go` | 58-78 StartChatRun；**67 WithoutCancel**；62/85/104/119/140/208/248/273/307/345/409/507/593/702/736 函数起点；325 AppendEvent；338-339 条件广播；182-184 seq 去重；205 页大小；273-284 终止事件；345-406 prepareChatRun（**缺串行校验**）；702-732 finishRun |
| `service/conversation/runbroker.go` | 10 缓冲 256；15 topic 上限；21-26 struct；49-56 topicLocked；58-93 Subscribe；95-123 Publish；125-144 Close；146-154 evict |
| `service/conversation/chat.go` | 60-103 buildMessageWithHistory；**66 ListMessages 快照** |
| `service/conversation/helper.go` | 158 historyWindowSize = 20 |
| `api/chat.go` | 39-78 ChatStream；80-125 ChatStreamRun；127-145 ChatRunActive；147-191 ChatResume；193-209 pumpRun；211-225 lastEventIDFrom |
| `router/router.go` | 51 stream/run；52 run/active；53 resume |
| `adaptor/repo/run/run.go` | 14-18 表职责；107-124 GetActiveBySession；145-172 AppendEvent；175 ListEventsAfter |
| `adaptor/repo/model/run.go` | 5-28 chat_runs；31-44 chat_run_events |
| `service/do/run.go` | 51 Seq = 事件游标 |
| `service/dto/chat.go` | 180 ChatRunInfo；199 ChatRunActiveResp.Run |
| `web/src/api/runStream.ts` | 36 key 前缀；42-81 activeRun 读写清；84-116 streamChatRun；120-152 resumeChatRun；156-173 attachRun；176-239 pumpRun；205/267 clearActiveRun；251-253 sessionId 覆写；285-295 fetchRunEvents；297-318 discoverActiveRun |
| `web/src/api/sse.ts` | 9-27 consumeSSE（无共享状态） |
| `web/src/api/client.ts` | 28-35 fetchAuthorized（无共享状态） |
| `web/src/App.tsx` | 239 isStreaming；272 controllerRef；277 isStreamingRef；298 单一节流 timer；300 reattachedSessionsRef；360-362 同步 effect；367-376 重挂 effect；555-557 loadSession 闸门；751-756 缓存失效用当前会话；758-761 / 815-818 / 844-847 / 871-873 闸门；909-913 提交闸门；941-944 / 990-997 提交生命周期；1024-1027 / 1052-1054 重挂闸门；1083-1085 / 1114-1116 重挂生命周期；1146-1151 / 1200-1202 setSessionId 串台；1243 / 1275-1276 resume 生命周期；1304-1319 节流队列；1347 滚动闸门；1354-1356 stopStreaming；1397 / 1472-1473 / 1507 传参 |
| `web/src/sessions/SessionPanel.tsx` | 18/38 isStreaming 参数；79 新建 disabled；109 会话项 disabled；132 分享 disabled；146 删除 disabled |
| `web/src/composer/Composer.tsx` | 7/16 isStreaming；32 本地闸门；61 文案；87-94 发送⇄停止同按钮 |
| `web/package.json` | 9 test 脚本（6 条并列） |
| `web/src/api/runStream.test.mjs` | 1-40 registerHooks + 浏览器替身（测试基建范式） |
