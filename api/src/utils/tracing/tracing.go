/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

OpenTelemetry 链路追踪：SDK 初始化与跨进程上下文传播。
*/

package tracing

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
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
		log.Printf("tracing: resource detection incomplete: %v", err)
	}

	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res), sdktrace.WithSampler(newSampler())}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != "" {
		// 读取标准 OTEL_EXPORTER_OTLP_* 环境变量；连接惰性建立，Tempo 不可用时不阻塞启动
		exporter, err := otlptracegrpc.New(ctx)
		if err != nil {
			log.Printf("tracing: OTLP exporter disabled: %v", err)
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
			log.Printf("tracing: shutdown: %v", err)
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

// TraceID 返回 ctx 中的 trace id，没有有效 span 时返回空字符串；
// *gin.Context 默认不会回退到 Request.Context()，需要单独处理
func TraceID(ctx context.Context) string {
	if c, ok := ctx.(*gin.Context); ok {
		if c == nil || c.Request == nil {
			return ""
		}
		ctx = c.Request.Context()
	}
	if ctx == nil {
		return ""
	}
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
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

// Logf 通过标准库 log 输出日志，ctx 中有 trace 时加上 "[trace=<id>] " 前缀；
// 供 cland、cloudlet 等未使用 go-logging 的进程使用，Lshortfile 仍指向调用方
func Logf(ctx context.Context, format string, args ...interface{}) {
	if id := TraceID(ctx); id != "" {
		format = "[trace=" + id + "] " + format
	}
	_ = log.Output(2, fmt.Sprintf(format, args...))
}

// Inject 将 ctx 中的 span 上下文写入 map，用于 gRPC stream 消息等无法经由 header 传播的场景
func Inject(ctx context.Context) map[string]string {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if len(carrier) == 0 {
		return nil
	}
	return carrier
}

// Extract 从 Inject 生成的 map 中恢复 span 上下文
func Extract(ctx context.Context, carrier map[string]string) context.Context {
	if len(carrier) == 0 {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(carrier))
}

// CommandName 返回命令中的脚本名（首个非环境变量赋值 token 的 basename），
// 用作 span 名称与属性，避免把含敏感参数的完整命令写入链路
func CommandName(command string) string {
	for _, field := range strings.Fields(command) {
		if strings.Contains(field, "=") {
			continue
		}
		return filepath.Base(field)
	}
	return ""
}
