package api

import (
	"context"
	"edu.agent.code/common"
	"edu.agent.code/service/consts"
	"edu.agent.code/service/dto"
	"errors"
	"github.com/gin-gonic/gin"
	"strconv"
	"strings"
	"time"
)

func (h *Handler) ChatCompletion(ctx *gin.Context) {
	var req dto.ChatRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		h.writeResp(ctx, nil, common.ParamError.WithError(err))
		return
	}
	user := h.authUser(ctx)
	req.UserID = user.UserID
	traceID := h.traceIDFrom(ctx)
	req.TraceID = traceID
	ctx.Set(common.CtxKeyTraceID, traceID)
	resp, err := h.session.ChatCompletion(ctx, req)
	if err != nil {
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}
	h.writeResp(ctx, resp, common.OK)
}

// ChatStream 发起一轮对话并以 SSE 返回事件。
//
// 关键改动：执行与请求解绑。这里只做两件事——先建 run（同步，失败可以正常返回 JSON），
// 再把 run 的事件转发给当前连接。客户端刷新/断网/nginx 超时只会让"转发"结束，
// 后台 run 照常跑到终态并把结果落库，前端重连后按 Last-Event-ID 补齐事件即可。
func (h *Handler) ChatStream(ctx *gin.Context) {
	var req dto.ChatRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		h.writeResp(ctx, nil, common.ParamError.WithError(err))
		return
	}
	user := h.authUser(ctx)
	req.UserID = user.UserID
	traceID := h.traceIDFrom(ctx)
	req.TraceID = traceID
	ctx.Set(common.CtxKeyTraceID, traceID)

	runID, sessionID, err := h.session.StartChatRun(ctx.Request.Context(), req)
	if err != nil {
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}

	writer := newStreamWriter(ctx)
	if writer == nil {
		h.writeResp(ctx, nil, common.ServerError.WithMsg("stream not supported"))
		return
	}
	stopHeartbeat := startHeartBeat(ctx, writer, traceID)
	defer stopHeartbeat()

	err = writer.writeEvent(consts.SseEventTypeReady, dto.ChatStreamEvent{
		Type:      consts.SseEventTypeReady,
		RunID:     runID,
		TraceID:   traceID,
		SessionID: sessionID,
		Message:   "stream open",
		Timestamp: time.Now().Format(time.DateTime),
	})
	if err != nil {
		// 流已经开始，不能再写 h.writeResp
		return
	}
	h.pumpRun(ctx, writer, runID, 0, traceID)
}

// ChatStreamRun 是断连续传入口：按 run_id 回放 afterSeq 之后的事件，并继续跟随实时事件。
//
// 游标来源二选一，Last-Event-ID 请求头优先于 ?after=。
func (h *Handler) ChatStreamRun(ctx *gin.Context) {
	user := h.authUser(ctx)
	traceID := h.traceIDFrom(ctx)
	ctx.Set(common.CtxKeyTraceID, traceID)

	runID := strings.TrimSpace(ctx.Query("run_id"))
	if runID == "" {
		h.writeResp(ctx, nil, common.ParamError.WithMsg("run_id is required"))
		return
	}
	afterSeq := lastEventIDFrom(ctx)

	// 归属校验：run_id 由客户端携带，不校验就会把别人的执行内容读出来。
	info, err := h.session.GetRunInfo(ctx.Request.Context(), user.UserID, runID)
	if err != nil {
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}
	if info == nil {
		h.writeResp(ctx, nil, common.ParamError.WithMsg("run not found"))
		return
	}

	writer := newStreamWriter(ctx)
	if writer == nil {
		h.writeResp(ctx, nil, common.ServerError.WithMsg("stream not supported"))
		return
	}
	stopHeartbeat := startHeartBeat(ctx, writer, traceID)
	defer stopHeartbeat()

	if err := writer.writeEvent(consts.SseEventTypeReady, dto.ChatStreamEvent{
		Type:      consts.SseEventTypeReady,
		RunID:     runID,
		TraceID:   traceID,
		SessionID: info.SessionID,
		Message:   "stream open",
		Timestamp: time.Now().Format(time.DateTime),
	}); err != nil {
		return
	}
	h.pumpRun(ctx, writer, runID, afterSeq, traceID)
}

// ChatRunActive 返回某会话下仍可续跑的 run（running / interrupted），供前端刷新后重新挂载。
func (h *Handler) ChatRunActive(ctx *gin.Context) {
	var req dto.ChatRunActiveReq
	if err := ctx.ShouldBindQuery(&req); err != nil {
		h.writeResp(ctx, nil, common.ParamError.WithError(err))
		return
	}
	user := h.authUser(ctx)
	if strings.TrimSpace(req.SessionID) == "" {
		h.writeResp(ctx, nil, common.ParamError.WithMsg("session_id is required"))
		return
	}
	info, err := h.session.GetActiveRun(ctx.Request.Context(), user.UserID, req.SessionID)
	if err != nil {
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}
	h.writeResp(ctx, &dto.ChatRunActiveResp{Run: info}, common.OK)
}

func (h *Handler) ChatResume(ctx *gin.Context) {
	var req dto.ChatResumeRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		h.writeResp(ctx, nil, common.ParamError.WithError(err))
		return
	}
	user := h.authUser(ctx)
	req.UserID = user.UserID
	traceID := h.traceIDFrom(ctx)
	req.TraceID = traceID
	ctx.Set(common.CtxKeyTraceID, traceID)

	afterSeq := lastEventIDFrom(ctx)

	runID, sessionID, runAfterSeq, err := h.session.StartResumeRun(ctx.Request.Context(), &req)
	if err != nil {
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}
	// 审批恢复时客户端手里已经有中断前的事件，默认从 run 当前游标继续；
	// 客户端显式带了 Last-Event-ID 则取更靠后的那个，避免重复渲染。
	if runAfterSeq > afterSeq {
		afterSeq = runAfterSeq
	}

	writer := newStreamWriter(ctx)
	if writer == nil {
		h.writeResp(ctx, nil, common.ServerError.WithMsg("stream not supported"))
		return
	}
	stopHeartbeat := startHeartBeat(ctx, writer, traceID)
	defer stopHeartbeat()

	if err := writer.writeEvent(consts.SseEventTypeReady, dto.ChatStreamEvent{
		Type:      consts.SseEventTypeReady,
		RunID:     runID,
		TraceID:   traceID,
		SessionID: sessionID,
		Message:   "stream open",
		Timestamp: time.Now().Format(time.DateTime),
	}); err != nil {
		return
	}
	h.pumpRun(ctx, writer, runID, afterSeq, traceID)
}

// pumpRun 把 run 的事件流转发给当前连接。
//
// 客户端断开时 PumpRun 返回 context.Canceled，这里静默返回即可——
// 后台 run 不受影响，这正是本次改造要保证的语义。
func (h *Handler) pumpRun(ctx *gin.Context, writer *streamWriter, runID string, afterSeq int64, traceID string) {
	err := h.session.PumpRun(ctx.Request.Context(), runID, afterSeq, writer.emitEvent)
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	_ = writer.writeEvent(consts.SseEventTypeError, dto.ChatStreamEvent{
		Type:      consts.SseEventTypeError,
		RunID:     runID,
		TraceID:   traceID,
		SessionID: "",
		Message:   err.Error(),
	})
}

// lastEventIDFrom 读取客户端上报的续传游标：Last-Event-ID 请求头优先，其次 ?after=。
func lastEventIDFrom(ctx *gin.Context) int64 {
	raw := strings.TrimSpace(ctx.GetHeader("Last-Event-ID"))
	if raw == "" {
		raw = strings.TrimSpace(ctx.Query("after"))
	}
	if raw == "" {
		return 0
	}
	seq, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seq < 0 {
		return 0
	}
	return seq
}
