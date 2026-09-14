package tracing

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/stats"
	"gopkg.in/macaron.v1"
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

func TestInjectExtractRoundTrip(t *testing.T) {
	ctx, span := Tracer().Start(context.Background(), "parent")
	defer span.End()

	carrier := Inject(ctx)
	if carrier["traceparent"] == "" {
		t.Fatalf("traceparent not injected: %v", carrier)
	}
	got := trace.SpanContextFromContext(Extract(context.Background(), carrier))
	want := span.SpanContext()
	if got.TraceID() != want.TraceID() || got.SpanID() != want.SpanID() || !got.IsRemote() {
		t.Errorf("extracted %+v, want remote trace=%s span=%s", got, want.TraceID(), want.SpanID())
	}

	if carrier := Inject(context.Background()); carrier != nil {
		t.Errorf("expected nil carrier without span, got %v", carrier)
	}
	if TraceID(Extract(context.Background(), nil)) != "" {
		t.Error("expected no trace id when carrier is empty")
	}
}

func TestGinMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GinMiddleware("test", "/skip")...)
	handler := func(c *gin.Context) { c.String(200, TraceID(c.Request.Context())) }
	r.GET("/api", handler)
	r.GET("/skip", handler)

	req := httptest.NewRequest("GET", "/api", nil)
	req.Header.Set("traceparent", testTraceParent)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Body.String() != testTraceID || rec.Header().Get(TraceIDHeader) != testTraceID {
		t.Errorf("continued trace: body=%q header=%q, want %q", rec.Body.String(), rec.Header().Get(TraceIDHeader), testTraceID)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/api", nil))
	if id := rec.Header().Get(TraceIDHeader); id == "" || id != rec.Body.String() {
		t.Errorf("new root trace: body=%q header=%q", rec.Body.String(), id)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/skip", nil))
	if rec.Body.String() != "" || rec.Header().Get(TraceIDHeader) != "" {
		t.Errorf("skipped path should not trace: body=%q header=%q", rec.Body.String(), rec.Header().Get(TraceIDHeader))
	}
}

func TestMacaronMiddleware(t *testing.T) {
	m := macaron.New()
	m.Use(MacaronMiddleware())
	m.Post("/internal/execute", func(c *macaron.Context) string {
		return TraceID(c.Req.Context())
	})

	req := httptest.NewRequest("POST", "/internal/execute", nil)
	req.Header.Set("traceparent", testTraceParent)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, req)
	if rec.Body.String() != testTraceID {
		t.Errorf("trace id in context = %q, want %q", rec.Body.String(), testTraceID)
	}

	rec = httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("POST", "/internal/execute", nil))
	if rec.Body.String() != "" {
		t.Errorf("request without traceparent should have no trace id, got %q", rec.Body.String())
	}
}

func TestSkipMethodFilter(t *testing.T) {
	filter := skipMethodFilter([]string{"CommandStream", "ReportHealth"})
	if filter(&stats.RPCTagInfo{FullMethodName: "/cloudland.v1.CloudletService/CommandStream"}) {
		t.Error("CommandStream should be skipped")
	}
	if !filter(&stats.RPCTagInfo{FullMethodName: "/cloudland.v1.ClandService/Execute"}) {
		t.Error("Execute should be traced")
	}
}

func TestCommandName(t *testing.T) {
	cases := map[string]string{
		"/opt/cloudland/scripts/backend/launch_vm.sh '1' 'secret'": "launch_vm.sh",
		"FOO=bar /opt/cloudland/scripts/backend/clear_vm.sh 3":     "clear_vm.sh",
		"": "",
	}
	for command, want := range cases {
		if got := CommandName(command); got != want {
			t.Errorf("CommandName(%q) = %q, want %q", command, got, want)
		}
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

func TestLogf(t *testing.T) {
	var buf bytes.Buffer
	flags, out := log.Flags(), log.Writer()
	log.SetFlags(log.Lshortfile)
	log.SetOutput(&buf)
	defer func() {
		log.SetFlags(flags)
		log.SetOutput(out)
	}()

	ctx, span := Tracer().Start(context.Background(), "caller")
	defer span.End()
	Logf(ctx, "hello %s", "world")
	Logf(context.Background(), "no trace")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines, got %q", buf.String())
	}
	if !strings.HasPrefix(lines[0], "tracing_test.go:") || !strings.HasSuffix(lines[0], "[trace="+span.SpanContext().TraceID().String()+"] hello world") {
		t.Errorf("traced line = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "tracing_test.go:") || !strings.HasSuffix(lines[1], ": no trace") {
		t.Errorf("untraced line = %q", lines[1])
	}
}

func TestTraceIDGinContext(t *testing.T) {
	ctx, span := Tracer().Start(context.Background(), "caller")
	defer span.End()
	gin.SetMode(gin.TestMode)
	gc, _ := gin.CreateTestContext(httptest.NewRecorder())
	gc.Request = httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	if got := TraceID(gc); got != span.SpanContext().TraceID().String() {
		t.Errorf("TraceID(*gin.Context) = %q, want %q", got, span.SpanContext().TraceID())
	}
}
