/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

History:
   Date     Who ID    Description
   -------- --- ---   -----------
   01/13/19 nanjj  Initial code

*/

package common

import (
	"context"

	"api/src/dbs"

	"gorm.io/gorm"
)

var (
	DB = dbs.DB
)

const (
	contextDBKey = "dbs"
)

func GetContextDB(ctx context.Context) (context.Context, *gorm.DB) {
	tx := ctx.Value(contextDBKey)
	if tx != nil {
		return ctx, tx.(*gorm.DB)
	}
	// 绑定请求 ctx 使 SQL 挂到链路上；去掉取消信号，避免客户端断开时中断数据库操作
	db := DB().WithContext(context.WithoutCancel(ctx))
	ctx = context.WithValue(ctx, contextDBKey, db)
	return ctx, db
}

func SetContextDB(ctx context.Context, db *gorm.DB) context.Context {
	ctx = context.WithValue(ctx, contextDBKey, db)
	return ctx
}

func StartTransaction(ctx context.Context) (context.Context, *gorm.DB, bool) {
	tx := ctx.Value(contextDBKey)
	if tx != nil {
		// returns old transaction
		return ctx, tx.(*gorm.DB), false
	}
	db := DB().WithContext(context.WithoutCancel(ctx)).Begin()
	ctx = context.WithValue(ctx, contextDBKey, db)
	// returns new transaction
	return ctx, db, true
}

func EndTransaction(ctx context.Context, err error) {
	tx := ctx.Value(contextDBKey)
	if tx != nil {
		db := tx.(*gorm.DB)
		if err != nil {
			db.Rollback()
		} else {
			db.Commit()
		}
	}
}
