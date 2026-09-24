package api

import (
	"edu.agent.code/common"
	"edu.agent.code/service/consts"
	"edu.agent.code/service/dto"
	"fmt"
	"github.com/gin-gonic/gin"
	"time"
)

func (h *Handler) ChatCompletion(c *gin.Context) {
	fmt.Println("chat finish")
	return
}

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

	writer := newStreamWriter(ctx)
	if writer == nil {
		h.writeResp(ctx, nil, common.ServerError.WithMsg("stream not supported"))
		return
	}
	stopHeartbeat := startHeartBeat(ctx, writer, traceID)
	defer stopHeartbeat()

	err := writer.write(consts.SseEventTypeReady, dto.ChatStreamEvent{
		Type:      consts.SseEventTypeReady,
		TraceID:   traceID,
		SessionID: "",
		Message:   "stream open",
		Timestamp: time.Now().Format(time.DateTime),
	})
	if err != nil {
		// 流已经开始，不能再写h.writeResp
		//h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}
	err = h.session.ChatStream(ctx.Request.Context(),
		req,
		func(event dto.ChatStreamEvent) error {
			return writer.write(event.Type, event)
		},
	)
	if err != nil {
		_ = writer.write(consts.SseEventTypeError, dto.ChatStreamEvent{
			Type:    consts.SseEventTypeError,
			TraceID: traceID,
			Message: err.Error(),
		})
	}
	return
}

func (h *Handler) ChatResume(c *gin.Context) {
	fmt.Println("resume finish")
	return
}
