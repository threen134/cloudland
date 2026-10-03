package tracing

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
)

const (
	gormSpanKey      = "tracing:span"
	gormParentCtxKey = "tracing:parent_ctx"
)

// GormPlugin 为 GORM 操作创建子 span：仅在 Statement.Context 带上游 span 时生效，
// 后台同步等任务的查询不产生孤立 trace；span 只记录带占位符的 SQL，不记录参数值
type GormPlugin struct{}

func (GormPlugin) Name() string { return "tracing" }

func (GormPlugin) Initialize(db *gorm.DB) error {
	cb := db.Callback()
	for _, err := range []error{
		cb.Create().Before("*").Register("tracing:before_create", gormBefore("create")),
		cb.Create().After("*").Register("tracing:after_create", gormAfter),
		cb.Query().Before("*").Register("tracing:before_query", gormBefore("query")),
		cb.Query().After("*").Register("tracing:after_query", gormAfter),
		cb.Update().Before("*").Register("tracing:before_update", gormBefore("update")),
		cb.Update().After("*").Register("tracing:after_update", gormAfter),
		cb.Delete().Before("*").Register("tracing:before_delete", gormBefore("delete")),
		cb.Delete().After("*").Register("tracing:after_delete", gormAfter),
		cb.Row().Before("*").Register("tracing:before_row", gormBefore("row")),
		cb.Row().After("*").Register("tracing:after_row", gormAfter),
		cb.Raw().Before("*").Register("tracing:before_raw", gormBefore("raw")),
		cb.Raw().After("*").Register("tracing:after_raw", gormAfter),
	} {
		if err != nil {
			return err
		}
	}
	return nil
}

func gormBefore(op string) func(*gorm.DB) {
	return func(tx *gorm.DB) {
		parent := tx.Statement.Context
		ctx, span := StartChild(parent, "gorm."+op, trace.WithSpanKind(trace.SpanKindClient))
		if !span.IsRecording() {
			return
		}
		tx.InstanceSet(gormParentCtxKey, parent)
		tx.InstanceSet(gormSpanKey, span)
		tx.Statement.Context = ctx
	}
}

func gormAfter(tx *gorm.DB) {
	v, ok := tx.InstanceGet(gormSpanKey)
	if !ok {
		return
	}
	span, ok := v.(trace.Span)
	if !ok {
		return
	}
	// 恢复父 ctx，避免同一 Statement 后续操作挂到已结束的 span 下
	if parent, ok := tx.InstanceGet(gormParentCtxKey); ok {
		if ctx, ok := parent.(context.Context); ok {
			tx.Statement.Context = ctx
		}
	}
	attrs := []attribute.KeyValue{
		attribute.String("db.system", tx.Dialector.Name()),
		attribute.Int64("db.rows_affected", tx.Statement.RowsAffected),
	}
	if tx.Statement.Table != "" {
		attrs = append(attrs, attribute.String("db.sql.table", tx.Statement.Table))
	}
	if sql := tx.Statement.SQL.String(); sql != "" {
		attrs = append(attrs, attribute.String("db.statement", sql))
	}
	span.SetAttributes(attrs...)
	err := tx.Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
	}
	EndSpan(span, err)
}
