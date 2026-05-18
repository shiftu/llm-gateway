package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const serviceName = "llm-gateway"

// Init sets up an OTLP gRPC TracerProvider pointed at endpoint and registers
// it as the global OTel tracer. Returns a shutdown function that flushes and
// stops the provider. When endpoint is empty, Init is a no-op: it returns a
// no-op shutdown and nil error so callers need no special-case logic.
func Init(ctx context.Context, endpoint string) (shutdown func(), err error) {
	if endpoint == "" {
		return func() {}, nil
	}

	conn, err := grpc.NewClient(
		endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: dial %s: %w", endpoint, err)
	}

	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("telemetry: create exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		// Non-fatal: fall back to default resource.
		res = resource.Default()
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	shutdownFn := func() {
		shutdownCtx := context.Background()
		_ = tp.Shutdown(shutdownCtx)
		_ = conn.Close()
	}
	return shutdownFn, nil
}
