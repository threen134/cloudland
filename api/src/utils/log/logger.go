/**
 * Licensed Materials - Property of PEG TECH INC
 *
 * (C) Copyright PEG TECH INC. 2024 All Rights Reserved
 * SPDX-License-Identifier: Apache-2.0

 * Contributors:
 *    bryan@raksmart.com - Initial implementation
 *
 *
 * Purpose: logging utilities
 *
**/

package log

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"gopkg.in/macaron.v1"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	logging "github.com/op/go-logging"
	"github.com/spf13/viper"
)

const (
	pkgLogID = "utils/log"
	// 带颜色，适合终端直接查看（裸机文件日志）
	defaultFormat = "%{color}%{time:2006-01-02 15:04:05.000 MST} [%{module}] %{shortfile} -> %{level:.4s} %{id:03x}%{color:reset} %{message}"
	// 无颜色，适合 Docker stdout / 日志采集（Loki/Promtail）
	plainFormat  = "%{time:2006-01-02T15:04:05.000Z07:00} [%{level:.4s}] [%{module}] %{shortfile} %{message}"
	defaultLevel = logging.INFO

	RequestIDKey = "X-Request-ID"
)

type JSONFormatter struct{}

func (f *JSONFormatter) Format(calldepth int, rec *logging.Record, w io.Writer) error {
	_, file, line, ok := runtime.Caller(calldepth)
	fileStr := "unknown"
	if ok {
		// Get short file name
		if idx := strings.LastIndex(file, "/"); idx >= 0 {
			fileStr = fmt.Sprintf("%s:%d", file[idx+1:], line)
		} else {
			fileStr = fmt.Sprintf("%s:%d", file, line)
		}
	}
	logData := map[string]interface{}{
		"time":   rec.Time.Format("2006-01-02T15:04:05.000Z07:00"),
		"level":  rec.Level.String(),
		"module": rec.Module,
		"file":   fileStr,
		"msg":    rec.Message(),
	}
	encoder := json.NewEncoder(w)
	return encoder.Encode(logData)
}

var (
	logger *logging.Logger

	defaultOutput *os.File

	modules map[string]string // Holds the map of all modules and their respective log level

	lock sync.RWMutex
	once sync.Once
)

func init() {
	logger = logging.MustGetLogger(pkgLogID)
	Reset()
}

// InitLogger sets up the logging backend based on the provided log file.
// which will read following configurations from the viper instance.
// logging.log_dir: log directory
// logging.log_level: log level: overall log level or module specific log level
//
//	(e.g. "debug", "info", "warn", "error", "fatal", "panic")
//	(e.g. "[<module>[,<module>...]=]<level>[:[<module>[,<module>...]=]<level>...]")
//
// logging.max_size: maximum size of log file
// logging.max_backups: maximum number of old log files to retain
// logging.max_age: maximum number of days to retain old log files
func InitLogger(log_file string) {
	if log_file == "" {
		log_file = "cl.log"
	}
	log_dir := viper.GetString("logging.log_dir")
	log_level := viper.GetString("logging.log_level")
	if log_level == "" {
		log_level = "info"
	}
	format := viper.GetString("logging.format")
	if format == "" {
		format = defaultFormat
	}

	// 当 log_dir 为空时，输出到 stdout（Docker 容器标准做法）
	// 配置了 log_dir 时才写入文件（裸机/非容器部署）
	if log_dir == "" {
		logger.Debugf("logging.log_dir not set, writing logs to stdout in JSON format")
		initBackend(&JSONFormatter{}, os.Stdout)
	} else {
		log_file = fmt.Sprintf("%s/%s", log_dir, log_file)
		max_size := viper.GetInt("logging.max_size")
		if max_size == 0 {
			max_size = 100
		}
		max_backups := viper.GetInt("logging.max_backups")
		if max_backups == 0 {
			max_backups = 10
		}
		max_age := viper.GetInt("logging.max_age")
		if max_age == 0 {
			max_age = 30
		}
		logger.Debugf("writing logs to file: %s (level=%s, maxSize=%d, maxBackups=%d, maxAge=%d)",
			log_file, log_level, max_size, max_backups, max_age)
		initRollingBackend(log_file, max_size, max_backups, max_age, format)
	}
	InitLogLevelFromSpec(log_level)
}

// Reset sets to logging to the defaults defined in this package.
func Reset() {
	modules = make(map[string]string)
	lock = sync.RWMutex{}

	defaultOutput = os.Stderr
	initBackend(SetFormat(defaultFormat), defaultOutput)
	InitLogLevelFromSpec("")
}

// SetFormat sets the logging format.
func SetFormat(formatSpec string) logging.Formatter {
	if formatSpec == "" {
		formatSpec = defaultFormat
	}
	return logging.MustStringFormatter(formatSpec)
}

// InitBackend sets up the logging backend based on
// the provided logging formatter and I/O writer.
func initBackend(formatter logging.Formatter, output io.Writer) {
	backend := logging.NewLogBackend(output, "", 0)
	backendFormatter := logging.NewBackendFormatter(backend, formatter)
	logging.SetBackend(backendFormatter).SetLevel(defaultLevel, "")
}

// initRollingBackend set rolling log backend
// maxSize is the maximum size in megabytes
// maxBackups is the maximum number of old log files to retain
// maxAge is the maximum number of days to retain old log files
func initRollingBackend(logfile string, maxSize int, maxBackups int, maxAge int, format string) {
	if logfile != "" {
		output := &lumberjack.Logger{
			Filename:   logfile,
			MaxSize:    maxSize,
			MaxBackups: maxBackups,
			MaxAge:     maxAge, //days
		}
		initBackend(SetFormat(defaultFormat), output)
	} else {
		initBackend(SetFormat(defaultFormat), defaultOutput)
	}
}

// DefaultLevel returns the fallback value for loggers to use if parsing fails.
func DefaultLevel() string {
	return defaultLevel.String()
}

// GetModuleLevel gets the current logging level for the specified module.
func GetModuleLevel(module string) string {
	// logging.GetLevel() returns the logging level for the module, if defined.
	// Otherwise, it returns the default logging level, as set by
	// `blogging/logging.go`.
	level := logging.GetLevel(module).String()
	return level
}

// SetModuleLevel sets the logging level for the modules that match the supplied
// regular expression. Can be used to dynamically change the log level for the
// module.
func SetModuleLevel(moduleRegExp string, level string) (string, error) {
	return setModuleLevel(moduleRegExp, level, true, false)
}

func setModuleLevel(moduleRegExp string, level string, isRegExp bool, revert bool) (string, error) {
	var re *regexp.Regexp
	logLevel, err := logging.LogLevel(level)
	if err != nil {
		logger.Warningf("Invalid logging level '%s' - ignored", level)
	} else {
		if !isRegExp || revert {
			logging.SetLevel(logLevel, moduleRegExp)
			logger.Debugf("Module '%s' logger enabled for log level '%s'", moduleRegExp, level)
		} else {
			re, err = regexp.Compile(moduleRegExp)
			if err != nil {
				logger.Warningf("Invalid regular expression: %s", moduleRegExp)
				return "", err
			}
			lock.Lock()
			defer lock.Unlock()
			for module := range modules {
				if re.MatchString(module) {
					logging.SetLevel(logging.Level(logLevel), module)
					modules[module] = logLevel.String()
					logger.Debugf("Module '%s' logger enabled for log level '%s'", module, logLevel)
				}
			}
		}
	}
	return logLevel.String(), err
}

// MustGetLogger is used in place of `logging.MustGetLogger` to allow us to
// store a map of all modules and submodules that have loggers in the system.
func MustGetLogger(module string) *logging.Logger {
	l := logging.MustGetLogger(module)
	lock.Lock()
	defer lock.Unlock()
	modules[module] = GetModuleLevel(module)
	return l
}

// InitLogLevelFromSpec initializes the logging based on the supplied spec. It is
// exposed externally so that consumers of the blogging package may parse their
// own logging specification. The logging specification has the following form:
//
//	[<module>[,<module>...]=]<level>[:[<module>[,<module>...]=]<level>...]
func InitLogLevelFromSpec(spec string) string {
	levelAll := defaultLevel
	var err error

	if spec != "" {
		fields := strings.Split(spec, ":")
		for _, field := range fields {
			split := strings.Split(field, "=")
			switch len(split) {
			case 1:
				if levelAll, err = logging.LogLevel(field); err != nil {
					logger.Warningf("Logging level '%s' not recognized, defaulting to '%s': %s", field, defaultLevel, err)
					levelAll = defaultLevel // need to reset cause original value was overwritten
				}
			case 2:
				// <module>[,<module>...]=<level>
				levelSingle, err := logging.LogLevel(split[1])
				if err != nil {
					logger.Warningf("Invalid logging level in '%s' ignored", field)
					continue
				}

				if split[0] == "" {
					logger.Warningf("Invalid logging override specification '%s' ignored - no module specified", field)
				} else {
					modules := strings.Split(split[0], ",")
					for _, module := range modules {
						logger.Debugf("Setting logging level for module '%s' to '%s'", module, levelSingle)
						logging.SetLevel(levelSingle, module)
					}
				}
			default:
				logger.Warningf("Invalid logging override '%s' ignored - missing ':'?", field)
			}
		}
	}

	logging.SetLevel(levelAll, "") // set the logging level for all modules

	// iterate through modules to reload their level in the modules map based on
	// the new default level
	for k := range modules {
		MustGetLogger(k)
	}
	// register blogging logger in the modules map
	MustGetLogger(pkgLogID)

	return levelAll.String()
}

// RequestID is a Gin middleware that injects a request ID into every request.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader(RequestIDKey)
		if requestID == "" {
			requestID = uuid.New().String()
		}
		c.Set(RequestIDKey, requestID)
		ctx := context.WithValue(c.Request.Context(), RequestIDKey, requestID)
		c.Request = c.Request.WithContext(ctx)
		c.Header(RequestIDKey, requestID)
		c.Next()
	}
}

// Logger is a Gin middleware that logs each request.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery
		c.Next()
		latency := time.Since(start)
		statusCode := c.Writer.Status()
		errorMessage := c.Errors.ByType(gin.ErrorTypePrivate).String()
		clientIP := c.ClientIP()
		method := c.Request.Method
		if raw != "" {
			path = path + "?" + raw
		}
		requestIDValue, exists := c.Get(RequestIDKey)
		requestID := "-"
		if exists {
			if str, ok := requestIDValue.(string); ok {
				requestID = str
			}
		}

		if viper.GetString("logging.log_dir") == "" {
			logData := map[string]interface{}{
				"time":       time.Now().Format("2006-01-02T15:04:05.000Z07:00"),
				"level":      "INFO",
				"module":     "apis",
				"tag":        "API_REQUEST",
				"method":     method,
				"path":       path,
				"status":     statusCode,
				"latency_ms": float64(latency.Nanoseconds()/1000) / 1000.0,
				"ip":         clientIP,
				"request_id": requestID,
			}
			if errorMessage != "" {
				logData["errors"] = errorMessage
			}
			jsonData, _ := json.Marshal(logData)
			fmt.Fprintln(os.Stdout, string(jsonData))
			return
		}

		logger.Infof("API REQUEST JSON: %s %s IP: %s RequestID: %s | %d %v | DATA: {\"method\":\"%s\",\"path\":\"%s\",\"status\":%d,\"latency_ms\":%.3f,\"ip\":\"%s\",\"request_id\":\"%s\",\"errors\":\"%s\"}",
			method, path, clientIP, requestID, statusCode, latency,
			method, path, statusCode, float64(latency.Nanoseconds())/1e6, clientIP, requestID, errorMessage)
	}
}

// MacaronLogger is a Macaron middleware that logs each request.
func MacaronLogger() macaron.Handler {
	return func(res http.ResponseWriter, req *http.Request, c *macaron.Context) {
		start := time.Now()
		c.Next()
		latency := time.Since(start)
		statusCode := c.Resp.Status()
		path := req.URL.Path
		raw := req.URL.RawQuery
		method := req.Method
		clientIP := c.RemoteAddr()
		if raw != "" {
			path = path + "?" + raw
		}

		// Macaron doesn't have a standard RequestID middleware in this project's context,
		// but we can try to get it from the header or generate a temporary one if needed.
		requestID := req.Header.Get(RequestIDKey)
		if requestID == "" {
			requestID = "-"
		}

		if viper.GetString("logging.log_dir") == "" {
			logData := map[string]interface{}{
				"time":       time.Now().Format("2006-01-02T15:04:05.000Z07:00"),
				"level":      "INFO",
				"module":     "routes",
				"tag":        "WEB_REQUEST",
				"method":     method,
				"path":       path,
				"status":     statusCode,
				"latency_ms": float64(latency.Nanoseconds()/1000) / 1000.0,
				"ip":         clientIP,
				"request_id": requestID,
			}
			jsonData, _ := json.Marshal(logData)
			fmt.Fprintln(os.Stdout, string(jsonData))
			return
		}

		logger.Infof("WEB REQUEST JSON: %s %s IP: %s | %d %v | DATA: {\"method\":\"%s\",\"path\":\"%s\",\"status\":%d,\"latency_ms\":%.3f,\"ip\":\"%s\",\"request_id\":\"%s\"}",
			method, path, clientIP, statusCode, latency,
			method, path, statusCode, float64(latency.Nanoseconds())/1e6, clientIP, requestID)
	}
}

// MacaronRequestID is a Macaron middleware that injects a request ID into every request.
func MacaronRequestID() macaron.Handler {
	return func(res http.ResponseWriter, req *http.Request, c *macaron.Context) {
		requestID := req.Header.Get(RequestIDKey)
		if requestID == "" {
			requestID = uuid.New().String()
		}
		// Macaron doesn't have a built-in context like Gin, but we can set it in the header
		// so that the logger and subsequent handlers can find it.
		req.Header.Set(RequestIDKey, requestID)
		res.Header().Set(RequestIDKey, requestID)
		c.Next()
	}
}
