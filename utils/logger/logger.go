package logger

import (
	"fmt"
	"log"
	"strings"
	"sync/atomic"

	"github.com/gogf/gf/v2/util/gconv"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	levelDebug int32 = 10
	levelInfo  int32 = 20
	levelWarn  int32 = 30
	levelError int32 = 40
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorGray   = "\033[90m"
)

var currentLevel atomic.Int32

func init() {
	currentLevel.Store(levelInfo)
}

func Init(level string) {
	switch strings.ToLower(level) {
	case "debug":
		currentLevel.Store(levelDebug)
	case "info":
		currentLevel.Store(levelInfo)
	case "warn":
		currentLevel.Store(levelWarn)
	case "error":
		currentLevel.Store(levelError)
	default:
		currentLevel.Store(levelInfo)
	}
}

func formatZapFields(fields []zap.Field) string {
	parts := make([]string, len(fields))
	for i, field := range fields {
		parts[i] = fmt.Sprintf("%s=%s", field.Key, getFieldValue(field))
	}
	return strings.Join(parts, ",")
}

func getFieldValue(f zap.Field) interface{} {
	switch f.Type {
	case zapcore.StringType, zapcore.StringerType:
		return f.String
	case zapcore.BoolType:
		return f.Integer == 1
	case zapcore.Int64Type, zapcore.Int32Type,
		zapcore.Uint32Type, zapcore.Uint64Type,
		zapcore.Int8Type, zapcore.Int16Type,
		zapcore.Uint8Type, zapcore.Uint16Type:
		return f.Integer
	case zapcore.Float32Type,
		zapcore.Float64Type:
		return gconv.Float64(f.Integer)
	default:
		return gconv.String(f.Interface)
	}
}

func formatLog(msg string, args ...interface{}) string {
	var fields []zap.Field
	var fmtArgs []interface{}
	for _, arg := range args {
		if f, ok := arg.(zap.Field); ok {
			fields = append(fields, f)
		} else {
			fmtArgs = append(fmtArgs, arg)
		}
	}
	result := msg
	if len(fmtArgs) > 0 {
		result = fmt.Sprintf(result, fmtArgs...)
	}
	if len(fields) > 0 {
		result += formatZapFields(fields)
	}
	return result
}

func Debug(format string, args ...interface{}) {
	if currentLevel.Load() <= levelDebug {
		log.Print("[Debug] " + formatLog(format, args...) + colorGray)
	}
}

func Info(format string, args ...interface{}) {
	if currentLevel.Load() <= levelInfo {
		log.Print("[INFO] " + formatLog(format, args...) + colorBlue)
	}
}

func Warn(format string, args ...interface{}) {
	if currentLevel.Load() <= levelWarn {
		log.Print("[Warn] " + formatLog(format, args...) + colorGray)
	}
}

func Error(format string, args ...interface{}) {
	if currentLevel.Load() <= levelError {
		log.Print("[Error] " + formatLog(format, args...) + colorRed)
	}
}
