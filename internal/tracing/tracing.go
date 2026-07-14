// Package tracing configures the process-wide OpenTelemetry tracer provider
// and exposes helpers for creating spans and propagating trace context to
// vendor HTTP calls. When OTEL_EXPORTER_OTLP_ENDPOINT is unset a no-op
// provider is installed so spans are recorded but never exported.
package tracing

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const serviceName = "aml-kyt-screening"

var (
	tracerMu       sync.Mutex
	tracerShutdown func(context.Context) error
)

// Install configures a global OTel tracer provider. If
// OTEL_EXPORTER_OTLP_ENDPOINT is set, an OTLP HTTP exporter is used; otherwise
// a no-op provider is installed (so spans are recorded but never exported).
// The returned shutdown function flushes and stops the provider and MUST be
// called on process exit. Install is idempotent: the first call wins and
// subsequent calls return the existing shutdown.
func Install(ctx context.Context) (shutdown func(context.Context) error, err error) {
	tracerMu.Lock()
	defer tracerMu.Unlock()
	if tracerShutdown != nil {
		return tracerShutdown, nil
	}
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	var tp *sdktrace.TracerProvider
	if endpoint == "" {
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithResource(newResource()),
		)
	} else {
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(endpoint)}
		if os.Getenv("OTEL_EXPORTER_OTLP_INSECURE") != "" {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		exp, expErr := otlptracehttp.New(ctx, opts...)
		if expErr != nil {
			return nil, fmt.Errorf("otlp exporter: %w", expErr)
		}
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exp),
			sdktrace.WithResource(newResource()),
		)
	}
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	tracerShutdown = tp.Shutdown
	return tp.Shutdown, nil
}

func newResource() *resource.Resource {
	r, _ := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		),
	)
	return r
}

// Tracer returns the global tracer named for this service.
func Tracer() trace.Tracer {
	return otel.Tracer(serviceName)
}

// Shutdown flushes and stops the global tracer provider, if installed. Safe to
// call when no provider is configured.
func Shutdown(ctx context.Context) error {
	tracerMu.Lock()
	sd := tracerShutdown
	tracerShutdown = nil
	tracerMu.Unlock()
	if sd == nil {
		return nil
	}
	return sd(ctx)
}

// StartSpan creates a child span with the given name on ctx. The returned
// context carries the span so downstream code can record child spans.
func StartSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, opts...)
}

// SpanFromContext returns the active span from ctx (nil if there is none).
func SpanFromContext(ctx context.Context) trace.Span {
	return trace.SpanFromContext(ctx)
}

// RecordError records err on the active span (if any) and sets its status to
// Error. Safe to call with a nil error.
func RecordError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	if span := SpanFromContext(ctx); span != nil && span.IsRecording() {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
}

// Propagate injects the trace context from ctx into req's headers so the
// downstream vendor call participates in the same trace.
func Propagate(ctx context.Context, req *http.Request) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
}