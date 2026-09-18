package dbs

import (
	"context"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	logrus "github.com/sirupsen/logrus"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"cpgateway/src/tracing"
)

var (
	locker        = sync.Mutex{}
	dbm           *gorm.DB
	testMode      = strings.HasSuffix(os.Args[0], ".test") || os.Getenv("db.testing") != ""
	objects       = []interface{}{}
	grades        = map[string]func(*gorm.DB) error{}
	needToUpgrade = false
	needToMigrate = false
)

func openDB() (db *gorm.DB) {
	dbType := getDBType()
	dbUrl := getDBUri()

	if testMode {
		// Tests use in-memory SQLite unless CPGATEWAY_TEST_DB_URI points at a PostgreSQL database.
		if uri := os.Getenv("CPGATEWAY_TEST_DB_URI"); uri != "" {
			dbType, dbUrl = "postgres", uri
		} else {
			dbType = "sqlite3"
			dbUrl = "file::memory:?cache=shared"
		}
	}
	if dbType == "" {
		dbType = "sqlite3"
		dbUrl = "cpgateway.db"
	}
	if dbUrl == "" {
		if dbType == "sqlite3" {
			dbUrl = "cpgateway.db"
		}
	}

	logLevel := gormlogger.Silent
	if testMode || getDBDebug() {
		logLevel = gormlogger.Info
	}
	gormCfg := &gorm.Config{
		Logger: gormlogger.New(
			log.New(os.Stdout, "\r\n", log.LstdFlags),
			gormlogger.Config{
				SlowThreshold: 200 * time.Millisecond,
				LogLevel:      logLevel,
				Colorful:      false,
			},
		),
	}

	var dialector gorm.Dialector
	switch dbType {
	case "postgres":
		dialector = postgres.Open(dbUrl)
	default:
		dialector = sqlite.Open(dbUrl)
	}

	var err error
	logrus.Infof("Opening database: type=%s", dbType)
	if db, err = gorm.Open(dialector, gormCfg); err != nil {
		logrus.Fatalf("Failed to open database: %v", err)
	}
	// 链路追踪：请求 ctx 带上游 span 时为 SQL 创建子 span
	if err = db.Use(tracing.GormPlugin{}); err != nil {
		logrus.Warnf("Failed to register tracing plugin: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		logrus.Fatalf("Failed to get underlying sql.DB: %v", err)
	}
	sqlDB.SetMaxIdleConns(getDBIdle())
	sqlDB.SetMaxOpenConns(getDBOpen())
	sqlDB.SetConnMaxLifetime(time.Minute * time.Duration(getDBLifetime()))

	return db
}

func newDB() *gorm.DB {
	locker.Lock()
	defer locker.Unlock()
	if dbm == nil {
		dbm = openDB()
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
	if !needToMigrate {
		return
	}
	logrus.Infof("Starting auto-migration for %d models", len(objects))
	for _, obj := range objects {
		if err := db.AutoMigrate(obj); err != nil {
			logrus.Errorf("Auto-migrate failed for %T: %v", obj, err)
		}
	}
	logrus.Info("Auto-migration completed")
	needToMigrate = false
}

func AutoUpgrade(name string, grade func(*gorm.DB) error) {
	locker.Lock()
	defer locker.Unlock()
	grades[name] = grade
	needToUpgrade = true
}

func doAutoUpgrade(db *gorm.DB) {
	if !needToUpgrade || len(grades) == 0 {
		return
	}
	logrus.Infof("Starting auto-upgrade for %d tasks", len(grades))
	names := make([]string, 0, len(grades))
	for name := range grades {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := grades[name](db); err != nil {
			logrus.Errorf("Auto-upgrade %s failed: %v", name, err)
		}
	}
	logrus.Info("Auto-upgrade completed")
	needToUpgrade = false
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

// DBContext 返回绑定请求 ctx 的 DB，使 SQL 挂到链路上；去掉取消信号，避免客户端断开时中断数据库操作
func DBContext(ctx context.Context) *gorm.DB {
	return DB().WithContext(context.WithoutCancel(ctx))
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
