package api

import (
	"edu.agent.code/service/dto"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http"
	"sync"
	"time"
)

type streamWriter struct {
	ctx     *gin.Context
	flusher http.Flusher
	mu      sync.Mutex
}

func newStreamWriter(c *gin.Context) *streamWriter {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	return &streamWriter{
		ctx:     c,
		flusher: flusher,
	}
}

func (w *streamWriter) write(eventType string, payload any) error {
	return w.writeFrame(eventType, payload, 0)
}

// writeEvent 写一条 run 事件。
//
// payload.Seq > 0 时额外写 SSE 的 `id:` 行：客户端据此记录 Last-Event-ID，
// 断线重连时用它做增量回放。ready / heartbeat 这类非 run 事件 Seq 为 0，
// 不写 id 行，免得把游标推到一个没有对应持久化事件的值上。
func (w *streamWriter) writeEvent(eventType string, payload dto.ChatStreamEvent) error {
	return w.writeFrame(eventType, payload, payload.Seq)
}

// emitEvent 让 streamWriter 直接满足 conversation.ChatEmit：
// 事件类型取 payload.Type，seq 取 payload.Seq（来自落库时分配的游标）。
func (w *streamWriter) emitEvent(payload dto.ChatStreamEvent) error {
	return w.writeEvent(payload.Type, payload)
}

func (w *streamWriter) writeFrame(eventType string, payload any, seq int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if seq > 0 {
		_, _ = fmt.Fprintf(w.ctx.Writer, "id: %d\n", seq)
	}
	_, _ = fmt.Fprintf(w.ctx.Writer, "event: %s\n", eventType)
	_, _ = fmt.Fprintf(w.ctx.Writer, "data: %s\n\n", data)
	w.flusher.Flush()
	return nil
}

func startHeartBeat(ctx *gin.Context, w *streamWriter, traceID string) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second * 15)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case now := <-ticker.C:
				_ = w.write("heartbeat", dto.ChatStreamEvent{
					Type:      "heartbeat",
					TraceID:   traceID,
					Message:   "stream alive",
					Timestamp: now.Format(time.RFC3339Nano),
				})
				return
				// 此处和PPT不同，PPT中如果断开连接可能会泄露goroutine
			case <-ctx.Request.Context().Done():
				return // 客户端断开，自动退出
			}
		}
	}()

	return func() { close(done) }
}
