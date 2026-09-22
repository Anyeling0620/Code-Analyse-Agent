package logger

import (
	"log"
	"strings"
	"sync/atomic"
)

const (
	levelDebug int32 = 10
	levelInfo  int32 = 20
	levelWarn  int32 = 30
	levelError int32 = 40
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
func Debug(format string, args ...interface{}) {
	if currentLevel.Load() <= levelDebug {
		log.Printf("[INFO] "+format, args...)
	}
}
func Info(format string, args ...interface{}) {
	if currentLevel.Load() <= levelInfo {
		log.Printf("[INFO] "+format, args...)
	}
}
func Warn(format string, args ...interface{}) {
	if currentLevel.Load() <= levelWarn {
		log.Printf("[INFO] "+format, args...)
	}
}
func Error(format string, args ...interface{}) {
	if currentLevel.Load() <= levelError {
		log.Printf("[INFO] "+format, args...)
	}
}
