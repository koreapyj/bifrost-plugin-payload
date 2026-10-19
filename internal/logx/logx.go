// Package logx is a tiny leveled logger used by the plugin. Bifrost does not
// hand a logger to dynamically loaded plugins, so messages go to stderr with a
// "[payload]" prefix. The minimum level is read from PAYLOAD_LOG_LEVEL
// (debug, info, warn, error; default info).
package logx

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
)

// Level is a log severity.
type Level int32

// Log levels.
const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var (
	minLevel atomic.Int32
	std      = log.New(os.Stderr, "[payload] ", log.LstdFlags|log.Lmsgprefix)
)

func init() {
	minLevel.Store(int32(ParseLevel(os.Getenv("PAYLOAD_LOG_LEVEL"))))
}

// ParseLevel converts a level name into a Level; unknown names map to info.
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

// SetLevel changes the minimum level at runtime.
func SetLevel(l Level) { minLevel.Store(int32(l)) }

// Enabled reports whether messages at level l are emitted.
func Enabled(l Level) bool { return int32(l) >= minLevel.Load() }

func logf(l Level, tag, format string, args ...any) {
	if !Enabled(l) {
		return
	}
	std.Output(3, tag+" "+fmt.Sprintf(format, args...)) //nolint:errcheck
}

// Debugf logs at debug level.
func Debugf(format string, args ...any) { logf(LevelDebug, "DEBUG", format, args...) }

// Infof logs at info level.
func Infof(format string, args ...any) { logf(LevelInfo, "INFO", format, args...) }

// Warnf logs at warn level.
func Warnf(format string, args ...any) { logf(LevelWarn, "WARN", format, args...) }

// Errorf logs at error level.
func Errorf(format string, args ...any) { logf(LevelError, "ERROR", format, args...) }
