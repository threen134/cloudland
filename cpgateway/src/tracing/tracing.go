// Package tracing 提供 OpenTelemetry 链路追踪：SDK 初始化、Gin 中间件、出站 HTTP 与 logrus 日志关联。
// 与 api/src/utils/tracing 行为一致（两个 Go module 独立，按方案复制实现）。
package tracing

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "cloudland"

// TraceIDHeader 响应头，返回本次请求的 trace id，便于用户复制排查
const TraceIDHeader = "X-Trace-ID"

// Init 初始化全局 TracerProvider 与 W3C 传播器，返回的函数用于退出前刷新未导出的 span。
// 未配置 OTEL_EXPORTER_OTLP_ENDPOINT 时不挂 exporter，但仍生成 trace_id 供日志关联；
// 初始化失败只打印告警，不影响服务启动。
func Init(ctx context.Context, serviceName, version string) (flush func()) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", serviceName),
			attribute.String("service.version", version),
		),
		resource.WithHost(),
		resource.WithFromEnv(), // OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES 可覆盖默认值
	)
	if err != nil {
		log.Warnf("tracing: resource detection incomplete: %v", err)
	}

	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res), sdktrace.WithSampler(newSampler())}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != "" {
		// 读取标准 OTEL_EXPORTER_OTLP_* 环境变量；连接惰性建立，Tempo 不可用时不阻塞启动
		exporter, err := otlptracegrpc.New(ctx)
		if err != nil {
			log.Warnf("tracing: OTLP exporter disabled: %v", err)
		} else {
			// 批量异步导出，队列满时丢弃，不阻塞业务
			opts = append(opts, sdktrace.WithBatcher(exporter))
		}
	}
	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)

	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tp.Shutdown(ctx); err != nil {
			log.Warnf("tracing: shutdown: %v", err)
		}
	}
}

// backgroundAttr 标记后台任务的 root span，采样器按 TRACING_BACKGROUND_SAMPLE_RATIO 单独采样
var backgroundAttr = attribute.Key("cloudland.background")

// newSampler 请求链路的根 span 按 TRACING_SAMPLE_RATIO（默认 1）采样，后台任务按 TRACING_BACKGROUND_SAMPLE_RATIO（默认 0.01）采样；
// 子 span 跟随父 span 的采样决定，保证链路完整
func newSampler() sdktrace.Sampler {
	return sdktrace.ParentBased(rootSampler{
		request:    sdktrace.TraceIDRatioBased(envRatio("TRACING_SAMPLE_RATIO", 1)),
		background: sdktrace.TraceIDRatioBased(envRatio("TRACING_BACKGROUND_SAMPLE_RATIO", 0.01)),
	})
}

type rootSampler struct {
	request    sdktrace.Sampler
	background sdktrace.Sampler
}

func (s rootSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	for _, kv := range p.Attributes {
		if kv.Key == backgroundAttr && kv.Value.AsBool() {
			return s.background.ShouldSample(p)
		}
	}
	return s.request.ShouldSample(p)
}

func (s rootSampler) Description() string { return "CloudlandRootSampler" }

func envRatio(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil && v >= 0 && v <= 1 {
		return v
	}
	return def
}

// StartBackground 为后台任务（周期同步、心跳、健康上报等）创建新的 root span，采样率由 TRACING_BACKGROUND_SAMPLE_RATIO 控制；
// 未采样时 span 不导出，但仍有 trace_id 可用于日志关联
func StartBackground(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if ctx == nil {
		ctx = context.Background()
	}
	opts = append(opts, trace.WithNewRoot(), trace.WithAttributes(backgroundAttr.Bool(true)))
	return Tracer().Start(ctx, name, opts...)
}

// Tracer 返回 cloudland 的 tracer
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// spanContext 取 ctx 中的 span 上下文；*gin.Context 默认不会回退到 Request.Context()，需要单独处理
func spanContext(ctx context.Context) trace.SpanContext {
	if c, ok := ctx.(*gin.Context); ok {
		if c == nil || c.Request == nil {
			return trace.SpanContext{}
		}
		ctx = c.Request.Context()
	}
	if ctx == nil {
		return trace.SpanContext{}
	}
	return trace.SpanContextFromContext(ctx)
}

// TraceID 返回 ctx 中的 trace id，没有有效 span 时返回空字符串
func TraceID(ctx context.Context) string {
	if sc := spanContext(ctx); sc.HasTraceID() {
		return sc.TraceID().String()
	}
	return ""
}

// StartChild 仅在 ctx 带上游 span 时创建子 span；否则返回原 ctx 与不记录的 span，避免后台任务产生孤立 trace
func StartChild(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx, trace.SpanFromContext(ctx)
	}
	return Tracer().Start(ctx, name, opts...)
}

// EndSpan 结束 span；err 非空时将 span 标记为错误
func EndSpan(span trace.Span, err error) {
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// GinMiddleware 返回创建 server span 并写入 X-Trace-ID 响应头的 Gin 中间件。
// skipPrefixes 中的路径前缀（健康检查等周期探测）不创建 span。
func GinMiddleware(service string, skipPrefixes ...string) []gin.HandlerFunc {
	filter := func(r *http.Request) bool {
		for _, prefix := range skipPrefixes {
			if strings.HasPrefix(r.URL.Path, prefix) {
				return false
			}
		}
		return true
	}
	return []gin.HandlerFunc{
		otelgin.Middleware(service, otelgin.WithFilter(filter)),
		func(c *gin.Context) {
			if id := TraceID(c.Request.Context()); id != "" {
				c.Header(TraceIDHeader, id)
			}
			c.Next()
		},
	}
}

// GinAccessLog 返回 gin 默认格式的访问日志，末尾追加 trace_id；需注册在 GinMiddleware 之后才能取到 span
func GinAccessLog() gin.HandlerFunc {
	return gin.LoggerWithFormatter(func(p gin.LogFormatterParams) string {
		if p.Latency > time.Minute {
			p.Latency = p.Latency.Truncate(time.Second)
		}
		traceID := ""
		if p.Request != nil {
			if id := TraceID(p.Request.Context()); id != "" {
				traceID = " | trace_id=" + id
			}
		}
		return fmt.Sprintf("[GIN] %v | %3d | %13v | %15s | %-7s %#v%s\n%s",
			p.TimeStamp.Format("2006/01/02 - 15:04:05"), p.StatusCode, p.Latency, p.ClientIP, p.Method, p.Path, traceID, p.ErrorMessage)
	})
}

// HTTPTransport 包装出站 HTTP 请求的 Transport：请求 ctx 中带上游 span 时创建 client span 并注入 traceparent；
// 没有上游上下文（后台同步任务）时直接透传，不产生孤立 trace。base 为 nil 时使用 http.DefaultTransport。
// 只用于内部服务（Region clapi 等），不要用于第三方地址，避免泄露 trace 头
func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base, otelhttp.WithFilter(func(r *http.Request) bool {
		return trace.SpanContextFromContext(r.Context()).IsValid()
	}))
}

// LogrusHook 为通过 log.WithContext(ctx) 输出的日志补充 trace_id、span_id 字段
type LogrusHook struct{}

func (LogrusHook) Levels() []log.Level { return log.AllLevels }

func (LogrusHook) Fire(entry *log.Entry) error {
	if entry.Context == nil {
		return nil
	}
	if sc := spanContext(entry.Context); sc.HasTraceID() {
		entry.Data["trace_id"] = sc.TraceID().String()
		entry.Data["span_id"] = sc.SpanID().String()
	}
	return nil
}
