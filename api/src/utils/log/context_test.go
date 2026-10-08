package log

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
)

const (
	testTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	testSpanID  = "00f067aa0ba902b7"
)

func testSpanContext(t *testing.T) context.Context {
	traceID, err := trace.TraceIDFromHex(testTraceID)
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := trace.SpanIDFromHex(testSpanID)
	if err != nil {
		t.Fatal(err)
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled})
	return trace.ContextWithSpanContext(context.Background(), sc)
}

func TestContextLoggerJSON(t *testing.T) {
	var buf bytes.Buffer
	initBackend(&JSONFormatter{}, &buf)
	defer Reset()

	l := MustGetLogger("test")
	ctx := testSpanContext(t)
	gin.SetMode(gin.TestMode)
	gc, _ := gin.CreateTestContext(httptest.NewRecorder())
	gc.Request = httptest.NewRequest("GET", "/", nil).WithContext(ctx)

	l.Ctx(ctx).Errorf("hello %s", "world")
	l.Ctx(ctx).Error("plain", 1)
	l.Ctx(gc).Infof("gin context")
	l.Ctx(context.Background()).Infof("no trace")
	l.Errorf("direct")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []struct{ msg, traceID, spanID string }{
		{"hello world", testTraceID, testSpanID},
		{"plain 1", testTraceID, testSpanID},
		{"gin context", testTraceID, testSpanID},
		{"no trace", "", ""},
		{"direct", "", ""},
	}
	if len(lines) != len(want) {
		t.Fatalf("expected %d log lines, got %d: %q", len(want), len(lines), buf.String())
	}
	for i, line := range lines {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("line %d is not JSON: %v", i, err)
		}
		if entry["msg"] != want[i].msg {
			t.Errorf("line %d msg = %v, want %q", i, entry["msg"], want[i].msg)
		}
		if id, _ := entry["trace_id"].(string); id != want[i].traceID {
			t.Errorf("line %d trace_id = %q, want %q", i, id, want[i].traceID)
		}
		if id, _ := entry["span_id"].(string); id != want[i].spanID {
			t.Errorf("line %d span_id = %q, want %q", i, id, want[i].spanID)
		}
		if file, _ := entry["file"].(string); !strings.HasPrefix(file, "context_test.go:") {
			t.Errorf("line %d file = %q, want caller context_test.go", i, file)
		}
	}
}
