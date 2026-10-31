package router

import (
	"edu.agent.code/common"
	"edu.agent.code/utils/tracing"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
)

// getTraceID 生成兜底 trace id（32 位 hex，不带连字符）。
//
// 历史上这里用的是 uuid.New().String()，即带连字符的 UUID。那个值与 span 的
// TraceID 完全无关，导致响应里回给用户的 trace_id 在 Jaeger 里搜不到——
// 面板上的 trace 是按 32 位 hex 索引的。现在只有在 span 上下文确实拿不到
// TraceID 时才走这条兜底，且格式与 Jaeger 对齐，保证两种来源都能直接检索。
func getTraceID() string {
	return common.GetUUIDHex()
}

func Tracing() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := otel.GetTextMapPropagator().Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))
		ctx, span := tracing.Tracer().Start(ctx, "http "+c.Request.Method+" "+c.FullPath())
		defer span.End()

		// trace_id 必须是 span 自己的 TraceID：它才是 Jaeger 里能检索的键。
		// 入站若带了 W3C traceparent，Extract 会复用父 traceId，这里也能自然继承。
		traceID := span.SpanContext().TraceID().String()
		if !span.SpanContext().HasTraceID() {
			traceID = c.GetString("trace_id")
			if traceID == "" {
				traceID = getTraceID()
			}
		}
		c.Set(common.CtxKeyTraceID, traceID)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
		// 出错才拿得到trace_id

		// 自定义添加
		span.SetAttributes(
			attribute.String("http.method", c.Request.Method),
			attribute.String("http.path", c.FullPath()),
			attribute.Int("http.status", c.Writer.Status()),
			attribute.String("trace_id", traceID),
		)

		if c.Writer.Status() >= 400 {
			span.SetAttributes(attribute.Bool("http.error", true))
		}

	}
}
