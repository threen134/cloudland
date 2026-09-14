/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

History:
   Date     Who ID    Description
   -------- --- ---   -----------
   01/13/19 nanjj  Initial code

*/

package dbs

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	rlog "api/src/utils/log"
	"api/src/utils/tracing"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

var logger = rlog.MustGetLogger("dbs")

var (
	locker          = sync.Mutex{}
	dbm             *gorm.DB
	testMode        = strings.HasSuffix(os.Args[0], ".test") || os.Getenv("db.testing") != ""
	objects         = []interface{}{}
	migrationErrors = map[string]string{}
	upgradeErrors   = map[string]string{}
	grades          = map[string]func(*gorm.DB) error{}
	needToUpgrade   = false
	needToMigrate   = false
	OpenDB          = openDB
)

func openDB() (db *gorm.DB) {
	dbType := cfg.GetType()
	dbUrl := cfg.GetUri()
	dbDebug := cfg.GetDebug()

	if testMode {
		dbType = "sqlite3"
		dbUrl = "file::memory:?cache=shared"
	}
	if dbType == "" {
		dbType = "sqlite3"
		dbUrl = "cland.db"
	}

	// Configure GORM logger
	logLevel := gormlogger.Silent
	if testMode || dbDebug {
		logLevel = gormlogger.Info
	}
	gormCfg := &gorm.Config{
		// 与 GORM v1 一致不建外键：大量关联字段以 0 或 -1 表示“无关联”，外键约束会拒绝这些写入
		DisableForeignKeyConstraintWhenMigrating: true,
		Logger: gormlogger.New(
			log.New(os.Stdout, "\r\n", log.LstdFlags),
			gormlogger.Config{
				SlowThreshold: 200 * time.Millisecond,
				LogLevel:      logLevel,
				Colorful:      false,
			},
		),
	}

	// Select dialector based on database type
	var dialector gorm.Dialector
	switch dbType {
	case "postgres":
		dialector = postgres.Open(dbUrl)
	default:
		dialector = sqlite.Open(dbUrl)
	}

	var err error
	fmt.Printf("Attempting to open database: type=%s, url=%s\n", dbType, dbUrl)
	if db, err = gorm.Open(dialector, gormCfg); err != nil {
		fmt.Printf("FAILED to open database: %v\n", err)
		panic(err)
	}
	fmt.Printf("Database connection established successfully\n")

	// 链路追踪：请求 ctx 带上游 span 时为 SQL 创建子 span
	if err = db.Use(tracing.GormPlugin{}); err != nil {
		fmt.Printf("FAILED to register tracing plugin: %v\n", err)
	}

	// Configure connection pool via underlying *sql.DB
	sqlDB, err := db.DB()
	if err != nil {
		fmt.Printf("FAILED to get underlying sql.DB: %v\n", err)
		panic(err)
	}
	idle := cfg.GetIdle()
	sqlDB.SetMaxIdleConns(idle)
	open := cfg.GetOpen()
	sqlDB.SetMaxOpenConns(open)
	lifetime := cfg.GetLifetime()
	sqlDB.SetConnMaxLifetime(time.Minute * time.Duration(lifetime))

	return db
}

func newDB() *gorm.DB {
	locker.Lock()
	defer locker.Unlock()
	if dbm == nil {
		dbm = OpenDB()
	}
	doAutoMigrate(dbm)
	doAutoUpgrade(dbm)
	return dbm
}

func AutoMigrate(values ...interface{}) {
	locker.Lock()
	defer locker.Unlock()
	objects = append(objects, values...)
	needToMigrate = true
}

func doAutoMigrate(db *gorm.DB) {
	if needToMigrate {
		logger.Infof("Starting database auto-migration for %d objects", len(objects))
		names := tableNames(db)
		for i := 0; i < len(objects); i++ {
			obj := objects[i]
			name := names[i]
			err := db.AutoMigrate(obj)
			if err != nil {
				logger.Error(err)
				msg := err.Error()
				if s, ok := migrationErrors[name]; ok {
					migrationErrors[name] = fmt.Sprintf("%s\n%s", s, msg)
				} else {
					migrationErrors[name] = msg
				}
			}
		}
		logger.Infof("Database auto-migration completed")
		needToMigrate = false
	}
}

func AutoUpgrade(name string, grade func(*gorm.DB) error) {
	locker.Lock()
	defer locker.Unlock()
	grades[name] = grade
	needToUpgrade = true
}

func doAutoUpgrade(db *gorm.DB) (err error) {
	if !needToUpgrade || len(grades) == 0 {
		return
	}
	logger.Infof("Starting database auto-upgrade for %d tasks", len(grades))
	names := []string{}
	for name := range grades {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		grade := grades[name]
		if grade == nil { // skip nil
			continue
		}
		if err = grade(db); err != nil {
			logger.Error(err)
			upgradeErrors[name] = err.Error()
			continue
		}
	}
	logger.Infof("Database auto-upgrade completed")
	needToUpgrade = false
	return
}

// DBContext 返回绑定请求 ctx 的 DB，使 SQL 挂到链路上；去掉取消信号，避免客户端断开时中断数据库操作
func DBContext(ctx context.Context) *gorm.DB {
	return DB().WithContext(context.WithoutCancel(ctx))
}

func DB() *gorm.DB {
	if dbm != nil {
		if needToMigrate || needToUpgrade {
			locker.Lock()
			if needToMigrate {
				doAutoMigrate(dbm)
			}
			if needToUpgrade {
				doAutoUpgrade(dbm)
			}
			locker.Unlock()
		}
		return dbm
	}
	return newDB()
}

func SetDB(db *gorm.DB) {
	locker.Lock()
	defer locker.Unlock()
	if db == dbm {
		return
	}
	needToMigrate = true
	needToUpgrade = true
	dbm = db
}

func TableNames() (names []string) {
	if dbm == nil {
		return
	}
	names = tableNames(dbm)
	return
}

func tableNames(db *gorm.DB) (names []string) {
	namer := schema.NamingStrategy{}
	for i := 0; i < len(objects); i++ {
		obj := objects[i]
		// Check if the model implements TableName() method (Tabler interface)
		if tabler, ok := obj.(interface{ TableName() string }); ok {
			names = append(names, tabler.TableName())
		} else {
			// Fall back to GORM's default naming convention
			names = append(names, namer.TableName(fmt.Sprintf("%T", obj)))
		}
	}
	return
}
