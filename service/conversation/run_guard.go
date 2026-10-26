package conversation

import (
	"context"
	"edu.agent.code/common"
	"edu.agent.code/utils/logger"
	"fmt"
	"sync"

	"go.uber.org/zap"
)

// ═══════════════════════════════════════════════════════════════════════════
// 配对约束（改动本文件或新增执行入口前必读）
//
// 本文件实现「同会话串行」：beginSessionRun 抢占某会话的执行权，endSessionRun
// 归还。这是**成对**原语，且释放必须由 defer 兜底，不能只在成功路径上手写一行。
//
// 一、任何调用 beginSessionRun / tryAcquire 的**新执行入口**，必须在同一函数内
//     注册 `defer s.endSessionRun(userID, sessionID)`。
//     判断标准不是"正常跑完要不要释放"，而是"这个函数无论以何种方式退出——
//     返回错误、panic、提前 return——会话权都必须归还"。手写释放一定会漏，
//     因为后续改动会在释放语句之前插入新的 return 分支。
//
//     若执行体是后台 goroutine：在**该 goroutine 内部**注册 defer，而不是在
//     起 goroutine 之前（父函数的 defer 会在 goroutine 尚未结束时提前执行）。
//     若执行体是阻塞式（在本调用栈跑完）：在成功抢占之后立刻注册 defer 即可。
//
//     抢占成功但后续准备步骤失败时也必须归还：prepareChatRun / prepareResumeRun
//     用具名返回值 + defer 判断 err != nil 来做这件事，原因同上——清理紧跟着
//     抢占，不依赖调用方记得处理。
//
// 二、defer 注册顺序必须是「先注册释放会话权，再注册 recoverRun」，即
//
//	defer s.endSessionRun(...)   // 先写
//	defer s.recoverRun(...)      // 后写
//
//	defer 后进先出，因此实际执行顺序是 **recoverRun 先、endSessionRun 后**：
//	panic 时先由 recoverRun 把 run 收敛到终态（failed）并关闭 topic，
//	然后才释放会话权。
//
//	反过来写（先注册 recoverRun、后注册释放）会造成一个窗口：
//	**会话已被下一个请求占用，而上一条 run 还没被标成终态**。此时新请求看到的是
//	一个"内存已放行、库里却仍是 running"的会话，串行约束被绕过，两条执行流会
//	同时读写同一份历史窗口。
//
// 三、漏配的后果（这就是为什么本节写在文件顶部）
//   - **该会话在本进程内永久 409**。tryAcquire 在查询之前就拒绝，泄漏的占位永远
//     不会被清掉；用户无论等多久、重开页面、换标签页都只会拿到 SessionBusy。
//   - **唯一现成的自愈机制是重启**：启动时 MarkRunningAsInterrupted 把遗留的
//     running 行标成 interrupted，新进程的内存表也是空的。也就是说锁表泄漏会把
//     "重启一下就好"变成唯一出路，而重启在生产上是高成本、有损的操作。
//   - 若同时还遗留了 running 行（例如 panic 后无人收敛），GetRunningBySession
//     这个只读兜底会**继续**拦截同一会话，两道闸门一起把会话钉死。
//
// 四、其它不变量
//   - endSessionRun 幂等：重复调用、或从未占用时调用都安全。因此 defer 可以放心
//     注册，不必先判断是否真的抢到了。
//   - bind 只在抢占成功之后调用（回填占用者 run_id）。它不会创建新占位：
//     若占位已不存在就什么都不做，避免在释放之后"复活"一个已死的占位。
//   - 锁表的生命周期与本进程一致，无容量上限、也无定时清理——正常路径全部靠
//     defer 释放，设计上不留 GC 兜底。这正是本节约束必须严格的原因。
//   - 只拦 running，不拦 interrupted（后者没有 goroutine 在跑，属于等待审批恢复）。
//     新增入口不要顺手把 interrupted 也算成"正在执行"。
//
// 五、当前已正确配对的入口（新增入口请照此对照，并在此处登记）
//   - StartChatRun     → run.go，后台 goroutine 内 defer 释放 + defer recoverRun（顺序符合第二条）
//   - StartResumeRun   → run.go，同上
//   - ChatCompletion   → chat.go，阻塞式，抢占成功后单条 defer 释放
//   - prepareChatRun   → run.go，抢占后用具名返回值 + defer 在 err != nil 时归还
//   - prepareResumeRun → run.go，同上（resumeAcquired 标志控制）
//
// 注意 ChatCompletion 与两个流式入口有一处**已知不对称**：它没有 recoverRun，
// 因此 panic 时锁会释放、但库里那条 run 会停在 running，被 GetRunningBySession
// 持续拦截（直到进程重启）。这是阻塞式路径的既有行为，改动它需要单独评估
// （例如补一条 defer 把 run 标成 failed），不在本注释的约束范围内。
// ═══════════════════════════════════════════════════════════════════════════

// SessionBusyError 在哨兵之上携带占用该会话的 run_id，便于前端/日志定位。
//
// 它实现 Is(target) 让 errors.Is(err, common.ErrSessionBusy) 成立，
// 因此调用方既可以按哨兵判定，也可以 errors.As 取到 RunID。
type SessionBusyError struct {
	SessionID string
	// RunID 是占用会话的那条 run；内存里刚抢占、还没落库时为空，
	// 由仓储兜底查到的则一定非空。
	RunID string
}

func (e *SessionBusyError) Error() string {
	if e == nil {
		return "conversation: session already has a running answer"
	}
	if e.RunID != "" {
		return fmt.Sprintf("conversation: session %s already has a running answer (run_id=%s)",
			e.SessionID, e.RunID)
	}
	return fmt.Sprintf("conversation: session %s already has a running answer", e.SessionID)
}

// Unwrap 让 errors.Is(err, common.ErrSessionBusy) 与 common.AsErrno 都能穿透到哨兵。
func (e *SessionBusyError) Unwrap() error { return common.ErrSessionBusy }

// sessionRunGuard 是「同会话串行」的进程内守卫。
//
// 为什么是内存锁而不是数据库唯一约束：
//   - 本服务的并发模型就是进程内的——run 由 goroutine 执行（run.go:68-76），
//     事件广播靠进程内 broker，单实例部署。进程内守卫能看见的东西已经覆盖全部执行者。
//   - 唯一约束要表达"同一 session 只能有一条 running"，就得建部分索引/生成列，
//     而 MySQL 与 sqlite（单测）对部分唯一索引的支持不一致，还会给 chat_runs
//     引入一次迁移；眼下没有多实例需求，不值得为它付这个代价。
//   - 内存守卫能在**查询之前**就拒绝，省掉一次无用的 DB 往返与历史消息读取。
//
// 但它不是唯一判据：进程重启后遗留的 running 行、或将来多实例写入的行，
// 内存结构看不见，因此 beginSessionRun 还会用 GetRunningBySession 做一次只读兜底。
//
// 零值可用（running map 懒初始化），这样所有以字面量构造 Service 的单测都不用改。
type sessionRunGuard struct {
	mu sync.Mutex
	// running 记录「正在执行的会话」→ 占用它的 run_id。
	// key 由 userID + sessionID 组成，避免不同用户的同名 session 互相阻塞。
	running map[string]string
}

// sessionRunKey 把用户与会话拼成不冲突的 key。
//
// 用 NUL 分隔：session_id 是十六进制 UUID，user_id 是业务 ID，都不可能包含 NUL，
// 因此不存在 "a" + "bc" 与 "ab" + "c" 拼成同一个 key 的可能。
func sessionRunKey(userID, sessionID string) string {
	return userID + "\x00" + sessionID
}

// tryAcquire 尝试占用会话执行权：成功返回 (占用者 run_id, true)，
// 失败（已有 run 在跑）返回 (占用者 run_id, false)。
func (g *sessionRunGuard) tryAcquire(userID, sessionID string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running == nil {
		g.running = make(map[string]string)
	}
	key := sessionRunKey(userID, sessionID)
	if holder, ok := g.running[key]; ok {
		return holder, false
	}
	// 先占位再落库：占位与 Create 之间的窗口是竞态的唯一入口，
	// 占位之后同一会话的第二个请求只会看到"已占用"。
	g.running[key] = ""
	return "", true
}

// bind 回填占用者的 run_id，供并发请求的错误信息与日志定位。
func (g *sessionRunGuard) bind(userID, sessionID, runID string) {
	if runID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running == nil {
		return
	}
	key := sessionRunKey(userID, sessionID)
	if _, ok := g.running[key]; ok {
		g.running[key] = runID
	}
}

// release 释放会话执行权。幂等：重复调用、或从未占用时调用都不会出错。
func (g *sessionRunGuard) release(userID, sessionID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.running, sessionRunKey(userID, sessionID))
}

// isRunning 仅用于测试与诊断，判断该会话是否被本进程占用。
func (g *sessionRunGuard) isRunning(userID, sessionID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.running[sessionRunKey(userID, sessionID)]
	return ok
}

// beginSessionRun 抢占「该会话的执行权」：已有 run 在跑时返回 SessionBusyError。
//
// 它只拦 running，**不拦 interrupted**：审批中断的 run 已经没有 goroutine 在执行，
// 用户此时继续提问、或点 resume 走 /api/chat/resume 都是合法路径。
// 这点与仓储的 GetActiveBySession（running + interrupted，供续传挂载用）必须区分开。
func (s *Service) beginSessionRun(ctx context.Context, userID, sessionID string) error {
	if userID == "" || sessionID == "" {
		return nil
	}
	holder, ok := s.sessionRuns.tryAcquire(userID, sessionID)
	if !ok {
		return &SessionBusyError{SessionID: sessionID, RunID: holder}
	}
	// 内存守卫只覆盖本进程起的 run。为了让「已有 running 行」这个事实无条件成立
	// （重启遗留、或别人直接写库），再用仓储做一次只读兜底。
	// 查询失败时放行：一次数据库抖动不该把用户的会话永久锁死。
	if s.runs != nil {
		existing, err := s.runs.GetRunningBySession(ctx, userID, sessionID)
		if err != nil {
			logger.Error("beginSessionRun: GetRunningBySession failed",
				zap.String("user_id", userID),
				zap.String("session_id", sessionID),
				zap.Error(err))
		} else if existing != nil {
			s.sessionRuns.release(userID, sessionID)
			return &SessionBusyError{SessionID: sessionID, RunID: existing.RunID}
		}
	}
	return nil
}

// endSessionRun 释放会话执行权。所有终态收敛点都要调用它（幂等）。
func (s *Service) endSessionRun(userID, sessionID string) {
	if userID == "" || sessionID == "" {
		return
	}
	s.sessionRuns.release(userID, sessionID)
}
