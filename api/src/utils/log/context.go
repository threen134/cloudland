/**
 * Purpose: trace 感知的日志封装
**/

package log

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	logging "github.com/op/go-logging"
	"go.opentelemetry.io/otel/trace"
)

// ModuleLogger 包装 go-logging 的 Logger，额外提供 Ctx 方法输出带 trace 信息的日志
type ModuleLogger struct {
	*logging.Logger
	// wrapped 的 ExtraCalldepth 多一层，抵消 ContextLogger 包装带来的栈帧，保证 shortfile 指向业务代码
	wrapped *logging.Logger
}

// ContextLogger 在每条日志前携带 ctx 中的 trace 信息：文本格式输出为 "[trace=<trace_id>] msg"，
// JSONFormatter 会将其提取为独立的 trace_id、span_id 字段
type ContextLogger struct {
	l   *logging.Logger
	tag *traceTag
}

// traceTag 作为 Record 的首个参数携带 trace 信息，类型不导出以免与业务参数混淆
type traceTag struct {
	traceID string
	spanID  string
}

func (t *traceTag) String() string { return "[trace=" + t.traceID + "]" }

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

// traceIDFromContext 返回 ctx 中的 trace id，没有有效 span 时返回空字符串
func traceIDFromContext(ctx context.Context) string {
	if sc := spanContext(ctx); sc.HasTraceID() {
		return sc.TraceID().String()
	}
	return ""
}

// Ctx 返回携带 ctx 中 trace 信息的日志器；ctx 中没有有效 span 时与直接调用 logger 等价
func (l *ModuleLogger) Ctx(ctx context.Context) *ContextLogger {
	c := &ContextLogger{l: l.wrapped}
	if sc := spanContext(ctx); sc.HasTraceID() {
		c.tag = &traceTag{traceID: sc.TraceID().String(), spanID: sc.SpanID().String()}
	}
	return c
}

func (c *ContextLogger) args(args []interface{}) []interface{} {
	if c.tag == nil {
		return args
	}
	return append([]interface{}{c.tag}, args...)
}

func (c *ContextLogger) format(format string) string {
	if c.tag == nil {
		return format
	}
	return "%v " + format
}

func (c *ContextLogger) Debug(args ...interface{}) { c.l.Debug(c.args(args)...) }

func (c *ContextLogger) Debugf(format string, args ...interface{}) {
	c.l.Debugf(c.format(format), c.args(args)...)
}

func (c *ContextLogger) Info(args ...interface{}) { c.l.Info(c.args(args)...) }

func (c *ContextLogger) Infof(format string, args ...interface{}) {
	c.l.Infof(c.format(format), c.args(args)...)
}

func (c *ContextLogger) Warning(args ...interface{}) { c.l.Warning(c.args(args)...) }

func (c *ContextLogger) Warningf(format string, args ...interface{}) {
	c.l.Warningf(c.format(format), c.args(args)...)
}

func (c *ContextLogger) Error(args ...interface{}) { c.l.Error(c.args(args)...) }

func (c *ContextLogger) Errorf(format string, args ...interface{}) {
	c.l.Errorf(c.format(format), c.args(args)...)
}

// splitTrace 取出 ContextLogger 附加的 trace 信息，并返回去掉 "[trace=<id>] " 前缀后的消息
func splitTrace(rec *logging.Record) (tag *traceTag, msg string) {
	msg = rec.Message()
	if len(rec.Args) > 0 {
		if t, ok := rec.Args[0].(*traceTag); ok {
			return t, strings.TrimPrefix(msg, t.String()+" ")
		}
	}
	return nil, msg
}
