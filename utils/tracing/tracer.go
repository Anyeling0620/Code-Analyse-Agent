package tracing

import (
	"context"
	"fmt"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"time"
)

type Config struct {
	ServerName     string
	Endpoint       string
	SampleRate     float64
	StdoutFallback bool
}

func Init(ctx context.Context, config Config) (func(context.Context) error, error) {
	return doInit(ctx, config)
}

func doInit(ctx context.Context, config Config) (func(context.Context) error, error) {
	var (
		exporter sdktrace.SpanExporter
		err      error
	)
	switch {
	case config.Endpoint != "":
		exporter, err = otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(config.Endpoint),
			otlptracegrpc.WithInsecure(),
			otlptracegrpc.WithTimeout(time.Second*5))
		if err != nil {
			return nil, fmt.Errorf("otlptracegrpc.New: %w", err)
		}
	case config.StdoutFallback:
		exporter, err = stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("stdouttrace.New: %w", err)
		}
	default:
		return func(ctx context.Context) error {
			return nil
		}, nil
	}

	var sampler sdktrace.Sampler
	switch {
	case config.SampleRate <= 0:
		sampler = sdktrace.NeverSample()
	case config.SampleRate >= 1:
		sampler = sdktrace.AlwaysSample()
	default:
		sampler = sdktrace.TraceIDRatioBased(config.SampleRate)

	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sampler),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(config.ServerName),
		)),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)
	return tp.Shutdown, nil
}
