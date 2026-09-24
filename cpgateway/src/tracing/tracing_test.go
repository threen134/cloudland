package tracing

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

const (
	testTraceID     = "4bf92f3577b34da6a3ce929d0e0e4736"
	testTraceParent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
)

func TestMain(m *testing.M) {
	// 不配置导出地址：只安装 TracerProvider 与传播器
	os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	os.Unsetenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	flush := Init(context.Background(), "test", "dev")
	code := m.Run()
	flush()
	os.Exit(code)
}

func TestGinMiddlewareAndAccessLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var access bytes.Buffer
	gin.DefaultWriter = &access
	defer func() { gin.DefaultWriter = os.Stdout }()

	r := gin.New()
	r.Use(GinMiddleware("test", "/health")...)
	r.Use(GinAccessLog())
	handler := func(c *gin.Context) { c.String(http.StatusOK, TraceID(c)) }
	r.GET("/api", handler)
	r.GET("/health", handler)

	req := httptest.NewRequest("GET", "/api", nil)
	req.Header.Set("traceparent", testTraceParent)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Body.String() != testTraceID || rec.Header().Get(TraceIDHeader) != testTraceID {
		t.Errorf("continued trace: body=%q header=%q, want %q", rec.Body.String(), rec.Header().Get(TraceIDHeader), testTraceID)
	}
	if !strings.Contains(access.String(), "trace_id="+testTraceID) {
		t.Errorf("access log missing trace id: %q", access.String())
	}

	access.Reset()
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Header().Get(TraceIDHeader) != "" || strings.Contains(access.String(), "trace_id=") {
		t.Errorf("skipped path should not trace: header=%q log=%q", rec.Header().Get(TraceIDHeader), access.String())
	}
}

func TestHTTPTransport(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("traceparent")
	}))
	defer srv.Close()
	client := &http.Client{Transport: HTTPTransport(nil)}

	ctx, span := Tracer().Start(context.Background(), "caller")
	defer span.End()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.Contains(got, span.SpanContext().TraceID().String()) {
		t.Errorf("traceparent = %q, want trace id %s", got, span.SpanContext().TraceID())
	}

	got = ""
	req, _ = http.NewRequest("GET", srv.URL, nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got != "" {
		t.Errorf("request without upstream span should not carry traceparent, got %q", got)
	}
}

func TestLogrusHook(t *testing.T) {
	var buf bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&buf)
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.AddHook(LogrusHook{})

	ctx, span := Tracer().Start(context.Background(), "caller")
	defer span.End()
	gin.SetMode(gin.TestMode)
	gc, _ := gin.CreateTestContext(httptest.NewRecorder())
	gc.Request = httptest.NewRequest("GET", "/", nil).WithContext(ctx)

	logger.WithContext(ctx).Info("with ctx")
	logger.WithContext(gc).Info("with gin ctx")
	logger.Info("without ctx")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 log lines, got %q", buf.String())
	}
	want := span.SpanContext().TraceID().String()
	for i, expect := range []string{want, want, ""} {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(lines[i]), &entry); err != nil {
			t.Fatalf("line %d is not JSON: %v", i, err)
		}
		if got, _ := entry["trace_id"].(string); got != expect {
			t.Errorf("line %d trace_id = %q, want %q", i, got, expect)
		}
	}
}
