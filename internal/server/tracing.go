package server

import (
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

const tracerName = "llm-gateway/http"

// TracingMiddleware wraps each HTTP request in an OTel span tagged with the
// request method and path. It uses the global TracerProvider set by
// telemetry.Init; when no provider is configured (no-op mode) the span is a
// no-op and adds zero overhead.
func TracingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracer := otel.Tracer(tracerName)
		ctx, span := tracer.Start(r.Context(), r.Method+" "+r.URL.Path)
		defer span.End()

		span.SetAttributes(
			attribute.String("http.method", r.Method),
			attribute.String("http.path", r.URL.Path),
		)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
