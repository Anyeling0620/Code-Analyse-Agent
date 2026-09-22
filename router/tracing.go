package router

import (
	"edu.agent.code/utils/tracing"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
)

func getTraceID() string {
	return uuid.New().String()
}

func Tracing() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := otel.GetTextMapPropagator().Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))
		ctx, span := tracing.Tracer().Start(ctx, "http "+c.Request.Method+" "+c.FullPath())
		defer span.End()

		traceID := c.GetString("trace_id")
		if traceID == "" {
			traceID = getTraceID()
		}
		c.Set("trace_id", traceID)
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
