package rpcs

import (
	"context"
	"testing"

	"api/src/utils/tracing"

	"go.opentelemetry.io/otel/trace"
)

func TestDispatchExecuteCallbackSpan(t *testing.T) {
	flush := tracing.Init(context.Background(), "test", "dev")
	defer flush()

	var got trace.SpanContext
	Add("trace_test_callback", func(ctx context.Context, args []string) (string, error) {
		got = trace.SpanContextFromContext(ctx)
		return "ok", nil
	})
	fb := &FrontbackService{}

	// 携带 cloudlet 回传的 trace 上下文：回调在其子 span 中执行
	parent := tracing.Extract(context.Background(), map[string]string{
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	})
	if _, err := fb.dispatchExecute(parent, "trace_test_callback", nil); err != nil {
		t.Fatal(err)
	}
	if got.TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" || got.SpanID().String() == "00f067aa0ba902b7" || got.IsRemote() {
		t.Errorf("callback should run in a local child span of the remote parent, got %+v", got)
	}

	// 无上游 trace 上下文（周期上报）：不创建 span
	got = trace.SpanContext{}
	if _, err := fb.dispatchExecute(context.Background(), "trace_test_callback", nil); err != nil {
		t.Fatal(err)
	}
	if got.IsValid() {
		t.Errorf("callback without upstream trace context should not start a span, got %+v", got)
	}
}
