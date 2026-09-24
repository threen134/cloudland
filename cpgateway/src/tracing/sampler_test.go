package tracing

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestRootSampler(t *testing.T) {
	t.Setenv("TRACING_SAMPLE_RATIO", "1")
	t.Setenv("TRACING_BACKGROUND_SAMPLE_RATIO", "0")
	tracer := sdktrace.NewTracerProvider(sdktrace.WithSampler(newSampler())).Tracer("test")

	_, request := tracer.Start(context.Background(), "request")
	if !request.SpanContext().IsSampled() {
		t.Error("request root span should be sampled")
	}
	_, background := tracer.Start(context.Background(), "job", trace.WithNewRoot(), trace.WithAttributes(backgroundAttr.Bool(true)))
	if background.SpanContext().IsSampled() || !background.SpanContext().HasTraceID() {
		t.Errorf("background span sampled=%v hasTraceID=%v, want unsampled with trace id",
			background.SpanContext().IsSampled(), background.SpanContext().HasTraceID())
	}
	ctx, parent := tracer.Start(context.Background(), "parent")
	_, child := tracer.Start(ctx, "child", trace.WithAttributes(backgroundAttr.Bool(true)))
	if !child.SpanContext().IsSampled() {
		t.Error("child span should follow the sampled parent")
	}
	child.End()
	parent.End()
}
