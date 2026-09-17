package tracing

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestGormPlugin(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	defer otel.SetTracerProvider(prev)

	db, err := gorm.Open(sqlite.Open("file:gorm_trace_test?mode=memory&cache=shared"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Use(GormPlugin{}); err != nil {
		t.Fatal(err)
	}
	type traceItem struct {
		ID   int64
		Name string
	}
	if err := db.AutoMigrate(&traceItem{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&traceItem{Name: "background"})
	if n := len(recorder.Ended()); n != 0 {
		t.Fatalf("queries without upstream span should not be traced, got %d spans", n)
	}

	ctx, parent := Tracer().Start(context.Background(), "request")
	db.WithContext(ctx).Create(&traceItem{Name: "a"})
	var items []traceItem
	db.WithContext(ctx).Where("name = ?", "secret-value").Find(&items)
	parent.End()

	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range recorder.Ended() {
		byName[s.Name()] = s
	}
	for _, name := range []string{"gorm.create", "gorm.query"} {
		s, ok := byName[name]
		if !ok {
			t.Fatalf("missing span %s, got %v", name, byName)
		}
		if s.Parent().SpanID() != parent.SpanContext().SpanID() {
			t.Errorf("%s parent = %s, want %s", name, s.Parent().SpanID(), parent.SpanContext().SpanID())
		}
		for _, kv := range s.Attributes() {
			if kv.Key == "db.statement" && strings.Contains(kv.Value.AsString(), "secret-value") {
				t.Errorf("%s statement leaks query parameter: %s", name, kv.Value.AsString())
			}
		}
	}
}
