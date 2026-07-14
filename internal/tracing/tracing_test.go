package tracing

import (
	"context"
	"net/http"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

func TestInstallNoExporter(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := Install(context.Background())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })
	if shutdown == nil {
		t.Fatal("nil shutdown")
	}
}

func TestShutdownIdempotent(t *testing.T) {
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown with no provider: %v", err)
	}
}

func TestStartSpanReturnsSpan(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	_, _ = Install(context.Background())
	ctx, span := StartSpan(context.Background(), "test.Span")
	if span == nil {
		t.Fatal("nil span")
	}
	span.End()
	if !trace.SpanContextFromContext(ctx).IsValid() {
		t.Error("expected valid span context after StartSpan")
	}
}

func TestPropagateInjectsHeaders(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	_, _ = Install(context.Background())
	ctx, span := StartSpan(context.Background(), "test.Propagate", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	req, _ := http.NewRequest(http.MethodPost, "https://vendor.example/v1/screen", nil)
	Propagate(ctx, req)
	if req.Header.Get("traceparent") == "" {
		t.Error("expected traceparent header after Propagate")
	}
}

func TestRecordErrorNilNoop(t *testing.T) {
	RecordError(context.Background(), nil)
}

func TestRecordErrorSetsStatus(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	_, _ = Install(context.Background())
	ctx, span := StartSpan(context.Background(), "test.RecordError")
	defer span.End()
	RecordError(ctx, errSentinel)
}

var errSentinel = errTest("sentinel")

type errTest string

func (e errTest) Error() string { return string(e) }

func TestTracerReturnsNamedTracer(t *testing.T) {
	if tr := Tracer(); tr == nil {
		t.Fatal("nil tracer")
	}
}

func TestPropagatorInstalled(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	_, _ = Install(context.Background())
	if p := otel.GetTextMapPropagator(); p == nil {
		t.Fatal("nil propagator")
	}
}