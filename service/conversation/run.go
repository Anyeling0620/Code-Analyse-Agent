package conversation

import (
	"context"
	"edu.agent.code/common"
	"edu.agent.code/service/consts"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"
	"edu.agent.code/service/tool/terminal"
	"edu.agent.code/utils/logger"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/jinzhu/copier"
	"go.uber.org/zap"
	"time"
)

// chatRunPrep 是 run 启动前同步准备好的上下文。
//
// 这些准备工作（读画像、读/建会话、拼历史消息）都在**请求 goroutine** 上完成，
// 好处是出错时能直接以 HTTP 错误返回；一旦进入后台执行，就只剩模型调用本身。
type chatRunPrep struct {
	runID        string
	checkpointID string
	userID       string
	traceID      string
	question     string
	session      *dto.SessionContext
	profile      *dto.Profile
	messages     []*schema.Message
}

// resumePrep 是审批恢复需要的参数快照。
type resumePrep struct {
	runID        string
	checkpointID string
	interruptID  string
	pendingID    string
	approved     bool
	userID       string
	sessionID    string
	traceID      string
	toolName     string
}

// StartChatRun 非阻塞地起一轮对话：
//  1. 在请求 ctx 上完成准备并落 chat_runs(running)；
//  2. 用 context.WithoutCancel 起后台执行（客户端断开只影响推送）；
//  3. 立刻返回 runID / sessionID，由上层去 PumpRun 转发事件。
func (s *Service) StartChatRun(ctx context.Context, req dto.ChatRequest) (string, string, error) {
	runCtx, prep, err := s.prepareChatRun(ctx, req)
	if err != nil {
		return "", "", err
	}
	bgCtx := context.WithoutCancel(runCtx)
	go func() {
		defer s.recoverRun(bgCtx, prep.runID)
		if _, err := s.executeRun(bgCtx, prep); err != nil {
			logger.Error("conversation run failed",
				zap.String("run_id", prep.runID),
				zap.String("session_id", prep.session.SessionID),
				zap.Error(err))
		}
	}()
	return prep.runID, prep.session.SessionID, nil
}

// StartResumeRun 非阻塞地恢复一次审批中断的 run。
//
// 返回的 afterSeq 是该 run 当前已落库的最后一个事件 seq：审批恢复时客户端手里
// 已经有全部历史事件，因此**默认从这里继续**，避免把整个 run 的事件再回放一遍
// 造成前端重复渲染。
func (s *Service) StartResumeRun(ctx context.Context, req *dto.ChatResumeRequest) (string, string, int64, error) {
	prep, afterSeq, runCtx, err := s.prepareResumeRun(ctx, req)
	if err != nil {
		return "", "", 0, err
	}
	bgCtx := context.WithoutCancel(runCtx)
	go func() {
		defer s.recoverRun(bgCtx, prep.runID)
		if err := s.executeResumeRun(bgCtx, prep); err != nil {
			logger.Error("conversation resume run failed",
				zap.String("run_id", prep.runID),
				zap.String("session_id", prep.sessionID),
				zap.Error(err))
		}
	}()
	return prep.runID, prep.sessionID, afterSeq, nil
}

// GetActiveRun 返回某会话下仍可续跑的 run（running / interrupted），没有则返回 nil。
func (s *Service) GetActiveRun(ctx context.Context, userID, sessionID string) (*dto.ChatRunInfo, error) {
	if s.runs == nil {
		return nil, nil
	}
	item, err := s.runs.GetActiveBySession(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	return toChatRunInfo(item), nil
}

// GetRunInfo 按 run_id 读取 run 摘要，并做归属校验。
//
// 非本人 run 一律返回 (nil, nil)，与"不存在"表现一致：
// 这样接口既不泄露别人的 run_id 是否存在，也避免越权读取执行内容。
func (s *Service) GetRunInfo(ctx context.Context, userID, runID string) (*dto.ChatRunInfo, error) {
	if s.runs == nil {
		return nil, nil
	}
	item, err := s.runs.GetByID(ctx, runID)
	if err != nil {
		return nil, err
	}
	if item == nil || item.UserID != userID {
		return nil, nil
	}
	return toChatRunInfo(item), nil
}

// PumpRun 把某个 run 的事件流推给 emit：先订阅实时事件，再回放 afterSeq 之后的
// 持久化事件（按 seq 去重），然后跟随实时事件直到 run 进入终态。
//
// 顺序很关键：**先订阅再回放**。若先回放再订阅，二者之间产生的事件会永久丢失；
// 反过来最多造成少数事件既走回放又走实时，用 seq 单调递增即可过滤。
//
// 客户端断开时 ctx.Done() 触发直接返回，后台执行不受任何影响。
func (s *Service) PumpRun(ctx context.Context, runID string, afterSeq int64, emit ChatEmit) error {
	if emit == nil {
		return errors.New("emit cannot be nil")
	}
	if s.runs == nil {
		return errors.New("run repository not initialized")
	}

	live, cancel := s.broker.Subscribe(runID)
	defer cancel()

	cursor := &runReplayCursor{seq: afterSeq}
	if err := s.replayRun(ctx, runID, cursor, emit); err != nil {
		return err
	}

	item, err := s.runs.GetByID(ctx, runID)
	if err != nil {
		return err
	}
	if item == nil {
		return fmt.Errorf("run %s not found", runID)
	}
	if item.Status != do.RunStatusRunning {
		// 已经不在执行中：回放完即可收尾，必要时补一条合成 error，
		// 免得客户端一直停在"等待中"。
		return s.finishReplayForInactiveRun(ctx, item, cursor, emit)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-live:
			if !ok {
				// topic 已关闭（run 结束）或本订阅者被判定跟不上：
				// 做最后一次回放兜底，保证不漏事件。
				if err := s.replayRun(ctx, runID, cursor, emit); err != nil {
					return err
				}
				return nil
			}
			if event.Seq <= cursor.seq {
				continue
			}
			if err := emit(event); err != nil {
				return err
			}
			cursor.seq = event.Seq
			cursor.lastType = event.Type
			cursor.any = true
			if isRunTerminalEvent(event.Type) {
				return nil
			}
		}
	}
}

// runReplayCursor 记录回放进度，避免重复下发同一条事件。
type runReplayCursor struct {
	seq      int64
	lastType string
	any      bool
}

const runReplayPageSize = 500

// replayRun 反复分页拉取 cursor.seq 之后的持久化事件，直到取空为止。
func (s *Service) replayRun(ctx context.Context, runID string, cursor *runReplayCursor, emit ChatEmit) error {
	for {
		events, err := s.runs.ListEventsAfter(ctx, runID, cursor.seq, runReplayPageSize)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		for _, record := range events {
			event, err := decodeRunEvent(record)
			if err != nil {
				logger.Error("replayRun decode event failed",
					zap.String("run_id", runID),
					zap.Int64("seq", record.Seq),
					zap.Error(err))
				cursor.seq = record.Seq
				continue
			}
			if err := emit(event); err != nil {
				return err
			}
			cursor.seq = event.Seq
			cursor.lastType = event.Type
			cursor.any = true
			if isRunTerminalEvent(event.Type) {
				return nil
			}
		}
		if len(events) < runReplayPageSize {
			return nil
		}
	}
}

// finishReplayForInactiveRun 处理"回放一个已经不在跑的 run"。
//
// run 可能是正常跑完（末事件是 done）、审批中断（末事件是 done，中断信息在
// interrupt 事件里），也可能是进程重启导致的中断——后者没有任何终止事件，
// 这时补一条合成 error，让客户端知道不用再等了。
func (s *Service) finishReplayForInactiveRun(
	ctx context.Context,
	item *do.ChatRun,
	cursor *runReplayCursor,
	emit ChatEmit,
) error {
	if cursor.any && isRunTerminalEvent(cursor.lastType) {
		return nil
	}
	event := dto.ChatStreamEvent{
		Type:        consts.SseEventTypeError,
		RunID:       item.RunID,
		SessionID:   item.SessionID,
		Message:     interruptedRunMessage,
		ContentKind: "markdown",
		RenderMode:  "append_block",
		Visibility:  "user",
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}
	return emit(event)
}

// interruptedRunMessage 用于服务重启/连接彻底丢失之后的收尾提示。
const interruptedRunMessage = "本轮执行因服务重启或连接中断而停止，可从现有内容继续追问，或重新发起。"

func isRunTerminalEvent(eventType string) bool {
	// 注意：interrupt 不是终止事件，而是"暂停"。
	// 审批中断后 run 还会继续产出收尾事件（暂停用的 tool_result、done），
	// 把 interrupt 当终止会让回放和实时转发都在此处提前收手，
	// 客户端就永远等不到 done。
	switch eventType {
	case consts.SseEventTypeDone, consts.SseEventTypeError:
		return true
	default:
		return false
	}
}

func decodeRunEvent(record do.ChatRunEvent) (dto.ChatStreamEvent, error) {
	var event dto.ChatStreamEvent
	if err := json.Unmarshal(record.Payload, &event); err != nil {
		return dto.ChatStreamEvent{}, err
	}
	// 落库时 Seq/RunID 已经写进 payload；这里以行数据为准，兼容早期记录。
	event.Seq = record.Seq
	if event.RunID == "" {
		event.RunID = record.RunID
	}
	if event.SessionID == "" {
		event.SessionID = record.SessionID
	}
	return event, nil
}

// publishRunEvent 是 run 内所有事件的唯一出口：先落库拿到 seq，再广播给在线客户端。
//
// 注意返回值的语义：它只反映"落库是否成功"，**与客户端是否还在线无关**。
// 这也是本次改造的核心——过去 emit 直连 HTTP 连接，客户端一断，
// 写失败会被当成 run 失败向上抛，整轮作废。
func (s *Service) publishRunEvent(ctx context.Context, runState *dto.ChatRunState, event dto.ChatStreamEvent) error {
	if runState == nil {
		return nil
	}
	event.RunID = runState.RunID
	if event.TraceID == "" {
		event.TraceID = runState.TraceID
	}
	if event.SessionID == "" {
		event.SessionID = runState.SessionID
	}

	if s.runs != nil && runState.RunID != "" {
		payload, err := json.Marshal(event)
		if err != nil {
			logger.Error("publishRunEvent marshal failed",
				zap.String("run_id", runState.RunID), zap.Error(err))
		} else {
			seq, appendErr := s.runs.AppendEvent(ctx, runState.RunID, runState.SessionID, runState.UserID, event.Type, payload)
			if appendErr != nil {
				logger.Error("publishRunEvent append failed",
					zap.String("run_id", runState.RunID), zap.Error(appendErr))
			} else {
				event.Seq = seq
				runState.LastSeq = seq
			}
		}
	}

	// 只有真正落库（有 seq）的事件才广播：没有 seq 的事件客户端无法定位，
	// 补发反而会破坏 Last-Event-ID 的单调性。
	if event.Seq > 0 {
		s.broker.Publish(runState.RunID, event)
	}
	return nil
}

// prepareChatRun 完成一轮对话的全部同步准备，并落一条 running 的 run 记录。
func (s *Service) prepareChatRun(ctx context.Context, req dto.ChatRequest) (context.Context, *chatRunPrep, error) {
	userID := req.UserID
	profile, err := s.prepareProfile(ctx, userID, req.Profile)
	if err != nil {
		logger.Error("prepareProfile failed", zap.Error(err), zap.Any("req", req))
		return nil, nil, err
	}
	session, err := s.prepareSession(ctx, userID, req.SessionID)
	if err != nil {
		logger.Error("prepareSession failed", zap.Error(err), zap.Any("req", req))
		return nil, nil, err
	}
	ctx = common.WithUserAndSession(ctx, userID, session.SessionID)
	updateProjectContextFromMessage(session, req.Message)
	ctx = withTurnSession(ctx, session)

	messages, err := s.buildMessageWithHistory(ctx, userID, session, profile, req.Message)
	if err != nil {
		logger.Error("buildMessageWithHistory failed", zap.Error(err), zap.Any("req", req))
		return nil, nil, err
	}

	runID := common.GetUUIDHex()
	checkpointID := fmt.Sprintf("session:%s turn:%s", session.SessionID, common.GetUUIDHex())
	ctx = common.WithCheckPointID(ctx, checkpointID)

	now := time.Now()
	prep := &chatRunPrep{
		runID:        runID,
		checkpointID: checkpointID,
		userID:       userID,
		traceID:      req.TraceID,
		question:     req.Message,
		session:      session,
		profile:      profile,
		messages:     messages,
	}
	if s.runs != nil {
		item := &do.ChatRun{
			RunID:        runID,
			TraceID:      req.TraceID,
			SessionID:    session.SessionID,
			UserID:       userID,
			Question:     req.Message,
			Status:       do.RunStatusRunning,
			CheckpointID: checkpointID,
			ProjectRoot:  session.CurrentProjectRoot,
			ProjectName:  session.CurrentProjectName,
			StartedAt:    now,
			UpdatedAt:    now,
		}
		if err := s.runs.Create(ctx, item); err != nil {
			logger.Error("create chat run failed", zap.Error(err), zap.Any("prep", prep))
			return nil, nil, err
		}
	}
	return ctx, prep, nil
}

// prepareResumeRun 校验并准备一次审批恢复，返回续传起点 afterSeq。
func (s *Service) prepareResumeRun(ctx context.Context, req *dto.ChatResumeRequest) (*resumePrep, int64, context.Context, error) {
	if req == nil {
		return nil, 0, nil, errors.New("nil resume request")
	}
	if req.PendingID == "" {
		logger.Error("StartResumeRun: Pending ID is empty", zap.Any("req", req))
		return nil, 0, nil, errors.New("PendingID is empty")
	}
	pending, err := s.approvals.GetByID(ctx, req.PendingID)
	if err != nil {
		logger.Error("StartResumeRun: GetByID", zap.Any("req", req), zap.Error(err))
		return nil, 0, nil, err
	}
	if req.SessionID != pending.SessionID {
		logger.Error("StartResumeRun: SessionID mismatch", zap.Any("req", req), zap.Any("pending", pending))
		return nil, 0, nil, errors.New("SessionID mismatch")
	}
	// 归属校验：pending_id 由客户端携带，凭它就能代为批准会让账号之间互相执行命令。
	if pending.UserID != req.UserID {
		logger.Error("StartResumeRun: pending approval not owned by user", zap.Any("req", req), zap.Any("pending", pending))
		return nil, 0, nil, errors.New("pending approval not owned by user")
	}
	if pending.CheckPointID == "" || pending.InterruptID == "" {
		logger.Error("StartResumeRun: checkpoint or interrupt id empty", zap.Any("req", req))
		return nil, 0, nil, errors.New("pending checkpointID or interruptID mismatch")
	}

	ctx = common.WithUserAndSession(ctx, pending.UserID, pending.SessionID)
	ctx = common.WithCheckPointID(ctx, pending.CheckPointID)

	prep := &resumePrep{
		checkpointID: pending.CheckPointID,
		interruptID:  pending.InterruptID,
		pendingID:    pending.ID,
		approved:     req.Approved,
		userID:       req.UserID,
		sessionID:    req.SessionID,
		traceID:      req.TraceID,
		toolName:     pending.Tool,
	}

	var afterSeq int64
	if s.runs != nil {
		// 优先复用被中断的那条 run：审批恢复在业务上是"同一轮对话的继续"，
		// 复用同一个 run_id 才能让客户端沿用同一个事件游标。
		existing, err := s.runs.GetByCheckpointID(ctx, pending.CheckPointID)
		if err != nil {
			logger.Error("StartResumeRun: GetByCheckpointID", zap.Error(err))
			return nil, 0, nil, err
		}
		now := time.Now()
		if existing == nil {
			existing = &do.ChatRun{
				RunID:        common.GetUUIDHex(),
				TraceID:      req.TraceID,
				SessionID:    req.SessionID,
				UserID:       req.UserID,
				Status:       do.RunStatusRunning,
				CheckpointID: pending.CheckPointID,
				StartedAt:    now,
				UpdatedAt:    now,
			}
			if err := s.runs.Create(ctx, existing); err != nil {
				logger.Error("StartResumeRun: create run failed", zap.Error(err))
				return nil, 0, nil, err
			}
		} else {
			afterSeq = existing.LastSeq
			if err := s.runs.UpdateStatus(ctx, existing.RunID, do.RunStatusRunning, map[string]any{
				"pending_approval_id": pending.ID,
				"ended_at":            nil,
			}); err != nil {
				logger.Error("StartResumeRun: UpdateStatus failed", zap.Error(err))
				return nil, 0, nil, err
			}
		}
		prep.runID = existing.RunID
	} else {
		prep.runID = common.GetUUIDHex()
	}

	return prep, afterSeq, ctx, nil
}

// executeRun 是真正跑一轮的同步核心：既被后台 goroutine 调用（StartChatRun），
// 也被阻塞式的 ChatCompletion 调用。
func (s *Service) executeRun(ctx context.Context, prep *chatRunPrep) (*dto.ChatResult, error) {
	runState := &dto.ChatRunState{
		UserID:      prep.userID,
		RunID:       prep.runID,
		TraceID:     prep.traceID,
		Question:    prep.question,
		SessionID:   prep.session.SessionID,
		ToolCallMap: map[string]dto.ToolCallState{},
	}
	emitRun := func(event dto.ChatStreamEvent) error {
		return s.publishRunEvent(ctx, runState, event)
	}

	_ = emitRun(dto.ChatStreamEvent{
		Type:      consts.SseEventTypeSession,
		SessionID: prep.session.SessionID,
		Stage:     "agent_start",
		Detail:    "runner.RUNNING",
	})

	iter := s.composeRunner.Run(ctx, prep.messages, adk.WithCheckPointID(prep.checkpointID))
	err := s.consumeAgentEvents(ctx, iter, runState, emitRun)
	// 保存会话 就算中断报错了 也要把 runState 存起来
	if err != nil {
		logger.Error("run failed", zap.Error(err), zap.String("run_id", prep.runID), zap.Any("runState", runState))
		if persistErr := s.persistSession(ctx, prep.session, runState); persistErr != nil {
			logger.Error("persistSession failed", zap.Error(persistErr), zap.String("run_id", prep.runID))
		}
		// 有半成品内容时标记为降级完成，前端据此显示"部分报告"而不是纯错误。
		if runState.Answer != "" || len(runState.RenderEvents) > 0 {
			runState.Degraded = true
		}
		_ = emitRun(dto.ChatStreamEvent{
			Type:        consts.SseEventTypeError,
			SessionID:   prep.session.SessionID,
			Message:     err.Error(),
			ContentKind: "markdown",
			RenderMode:  "append_block",
			Visibility:  "user",
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
		})
		s.finishRun(ctx, runState, prep.session, do.RunStatusFailed, err.Error())
		return nil, err
	}

	result := &dto.ChatResult{
		Answer:           runState.Answer,
		ReasoningContent: runState.ReasoningContent,
		SessionID:        prep.session.SessionID,
		UsedTools:        runState.UsedTools,
		Profile:          prep.profile,
		Session:          prep.session,
	}

	// 不管是否成功 都需要保存会话
	if persistErr := s.persistSession(ctx, prep.session, runState); persistErr != nil {
		logger.Error("persistSession failed", zap.Error(persistErr), zap.String("run_id", prep.runID))
	}

	_ = emitRun(dto.ChatStreamEvent{
		Type:       consts.SseEventTypeDone,
		SessionID:  prep.session.SessionID,
		Timestamp:  time.Now().Format(time.DateTime),
		Visibility: "user",
		Result:     result,
	})

	// 审批中断在业务上是"暂停"而不是终态：run 记为 interrupted，
	// 保留 checkpoint / pending，等审批接口恢复同一条 run。
	if runState.Interrupted {
		s.finishRun(ctx, runState, prep.session, do.RunStatusInterrupted, "")
		return result, nil
	}
	s.finishRun(ctx, runState, prep.session, do.RunStatusDone, "")
	return result, nil
}

// executeResumeRun 执行一次审批恢复，事件仍然发到**同一条 run**上。
func (s *Service) executeResumeRun(ctx context.Context, prep *resumePrep) error {
	runState := &dto.ChatRunState{
		UserID:      prep.userID,
		RunID:       prep.runID,
		TraceID:     prep.traceID,
		SessionID:   prep.sessionID,
		UsedTools:   []string{prep.toolName},
		ToolCallMap: map[string]dto.ToolCallState{},
	}
	emitRun := func(event dto.ChatStreamEvent) error {
		return s.publishRunEvent(ctx, runState, event)
	}

	_ = emitRun(dto.ChatStreamEvent{
		Type:      consts.SseEventTypeSession,
		SessionID: prep.sessionID,
		Stage:     "resume_chat_started",
	})

	iter, err := s.composeRunner.ResumeWithParams(ctx, prep.checkpointID, &adk.ResumeParams{
		Targets: map[string]any{
			prep.interruptID: terminal.ApprovalDecision{
				PendingID: prep.pendingID,
				Approved:  prep.approved,
			},
		},
	})
	if err != nil {
		logger.Error("executeResumeRun: composeRunner failed", zap.String("run_id", prep.runID), zap.Error(err))
		_ = emitRun(dto.ChatStreamEvent{
			Type:        consts.SseEventTypeError,
			SessionID:   prep.sessionID,
			Message:     err.Error(),
			ContentKind: "markdown",
			RenderMode:  "append_block",
			Visibility:  "user",
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
		})
		s.finishRun(ctx, runState, nil, do.RunStatusFailed, err.Error())
		return err
	}

	if err := s.consumeAgentEvents(ctx, iter, runState, emitRun); err != nil {
		logger.Error("executeResumeRun: consumeAgentEvents failed", zap.String("run_id", prep.runID), zap.Error(err))
		if runState.Answer != "" || len(runState.RenderEvents) > 0 {
			runState.Degraded = true
		}
		_ = emitRun(dto.ChatStreamEvent{
			Type:        consts.SseEventTypeError,
			SessionID:   prep.sessionID,
			Message:     err.Error(),
			ContentKind: "markdown",
			RenderMode:  "append_block",
			Visibility:  "user",
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
		})
		s.finishRun(ctx, runState, nil, do.RunStatusFailed, err.Error())
		return err
	}

	answer := runState.Answer
	if answer == "" {
		if prep.approved {
			answer = "命令已按用户批注执行完毕"
		} else {
			answer = "用户拒绝执行"
		}
		_ = emitRun(dto.ChatStreamEvent{
			Type:        consts.SseEventTypeDelta,
			SessionID:   prep.sessionID,
			Delta:       answer,
			ContentKind: "markdown",
			RenderMode:  "append_block",
			Visibility:  "user",
			Timestamp:   time.Now().Format(time.DateTime),
		})
	}

	_ = emitRun(dto.ChatStreamEvent{
		Type:      consts.SseEventTypeDone,
		SessionID: prep.sessionID,
		Result: &dto.ChatResult{
			Answer:    answer,
			SessionID: prep.sessionID,
			UsedTools: runState.UsedTools,
		},
	})

	renderEvent := make([]do.ChatStreamEvent, 0, len(runState.RenderEvents))
	_ = copier.Copy(&renderEvent, &runState.RenderEvents)
	if err := s.sessions.AppendMessage(ctx, &do.ChatMessageRecord{
		SessionID:    prep.sessionID,
		UserID:       prep.userID,
		Role:         string(schema.Assistant),
		Content:      answer,
		RenderEvents: renderEvent,
		CreatedAt:    time.Now(),
	}); err != nil {
		logger.Error("executeResumeRun: AppendMessage failed", zap.String("run_id", prep.runID), zap.Error(err))
	}

	s.finishRun(ctx, runState, nil, do.RunStatusDone, "")
	return nil
}

// finishRun 收敛一条 run：回填终态字段、广播收尾、关闭 topic。
func (s *Service) finishRun(ctx context.Context, runState *dto.ChatRunState, session *dto.SessionContext, status, errMsg string) {
	if runState == nil || runState.RunID == "" {
		return
	}
	if s.runs != nil {
		extra := map[string]any{
			"answer":              runState.Answer,
			"degraded":            runState.Degraded,
			"error_msg":           errMsg,
			"last_seq":            runState.LastSeq,
			"pending_approval_id": runState.PendingApprovalID,
		}
		if session != nil {
			extra["project_root"] = session.CurrentProjectRoot
			extra["project_name"] = session.CurrentProjectName
		}
		if do.IsTerminalRunStatus(status) {
			now := time.Now()
			extra["ended_at"] = &now
		}
		if err := s.runs.UpdateStatus(ctx, runState.RunID, status, extra); err != nil {
			logger.Error("finishRun UpdateStatus failed",
				zap.String("run_id", runState.RunID), zap.String("status", status), zap.Error(err))
		}
	}
	if s.broker != nil {
		s.broker.Close(runState.RunID)
	}
}

// recoverRun 兜住后台 goroutine 里的 panic：否则一条 run 会永远停在 running，
// 订阅它的客户端也会一直等待。
func (s *Service) recoverRun(ctx context.Context, runID string) {
	recovered := recover()
	if recovered == nil {
		return
	}
	logger.Error("conversation run panic recovered",
		zap.String("run_id", runID),
		zap.Any("panic", recovered))
	if s.runs == nil || runID == "" {
		return
	}
	msg := fmt.Sprintf("run panic: %v", recovered)
	noPanicCtx := context.WithoutCancel(ctx)
	now := time.Now()
	if err := s.runs.UpdateStatus(noPanicCtx, runID, do.RunStatusFailed, map[string]any{
		"error_msg":  msg,
		"updated_at": now,
		"ended_at":   &now,
	}); err != nil {
		logger.Error("recoverRun UpdateStatus failed", zap.String("run_id", runID), zap.Error(err))
	}
	s.broker.Close(runID)
}

func toChatRunInfo(item *do.ChatRun) *dto.ChatRunInfo {
	if item == nil {
		return nil
	}
	info := &dto.ChatRunInfo{
		RunID:             item.RunID,
		SessionID:         item.SessionID,
		Status:            item.Status,
		Degraded:          item.Degraded,
		Question:          item.Question,
		PendingApprovalID: item.PendingApprovalID,
		ProjectRoot:       item.ProjectRoot,
		ProjectName:       item.ProjectName,
		LastSeq:           item.LastSeq,
	}
	if !item.StartedAt.IsZero() {
		info.StartedAt = item.StartedAt.Format(time.RFC3339)
	}
	if item.EndedAt != nil && !item.EndedAt.IsZero() {
		info.EndedAt = item.EndedAt.Format(time.RFC3339)
	}
	return info
}
