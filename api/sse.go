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
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := json.Marshal(payload)
	if err != nil {
		return err
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
			}
		}
	}()

	return func() { close(done) }
}
