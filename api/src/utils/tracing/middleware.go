/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

HTTP / gRPC 链路追踪中间件。
*/

package tracing

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"
	"gopkg.in/macaron.v1"
)

// TraceIDHeader 响应头，返回本次请求的 trace id，便于用户复制排查
const TraceIDHeader = "X-Trace-ID"

// GinMiddleware 返回创建 server span 并写入 X-Trace-ID 响应头的 Gin 中间件。
// skipPrefixes 中的路径前缀（版本查询、周期轮询）不创建 span。
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

// MacaronMiddleware 提取上游（cland 回调）通过 traceparent 头传入的 trace 上下文，写入 c.Req.Context()。
// 这里不创建 span，由回调命令解码后按需创建，避免周期上报产生无效 trace。
func MacaronMiddleware() macaron.Handler {
	return func(c *macaron.Context) {
		ctx := otel.GetTextMapPropagator().Extract(c.Req.Context(), propagation.HeaderCarrier(c.Req.Header))
		c.Req.Request = c.Req.WithContext(ctx)
		c.Map(c.Req.Request)
		c.Next()
	}
}

// GRPCServerOption 返回 gRPC server 的链路追踪 stats handler，skipMethods 中的方法（长连接流、周期上报）不产生 span
func GRPCServerOption(skipMethods ...string) grpc.ServerOption {
	return grpc.StatsHandler(otelgrpc.NewServerHandler(otelgrpc.WithFilter(skipMethodFilter(skipMethods))))
}

// GRPCDialOption 返回 gRPC client 的链路追踪 stats handler
func GRPCDialOption() grpc.DialOption {
	return grpc.WithStatsHandler(otelgrpc.NewClientHandler())
}

// HTTPTransport 包装出站 HTTP 请求的 Transport：请求 ctx 中带上游 span 时创建 client span 并注入 traceparent；
// 没有上游上下文（后台周期任务）时直接透传，不产生孤立 trace。base 为 nil 时使用 http.DefaultTransport。
// 只用于内部服务（alarm-rules-manager、Prometheus 等），不要用于第三方地址，避免泄露 trace 头
func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base, otelhttp.WithFilter(func(r *http.Request) bool {
		return trace.SpanContextFromContext(r.Context()).IsValid()
	}))
}

func skipMethodFilter(skipMethods []string) otelgrpc.Filter {
	return func(info *stats.RPCTagInfo) bool {
		for _, method := range skipMethods {
			if strings.HasSuffix(info.FullMethodName, "/"+method) {
				return false
			}
		}
		return true
	}
}
