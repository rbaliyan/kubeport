package cli

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// resolveLogLevel picks the effective slog level. --verbose wins over any
// config setting; otherwise the parsed config value is used; otherwise Info.
func (a *app) resolveLogLevel() slog.Level {
	if a.verbose {
		return slog.LevelDebug
	}
	if a.cfg != nil {
		if lvl, ok := parseLogLevel(a.cfg.LogLevel); ok {
			return lvl
		}
	}
	return slog.LevelInfo
}

// parseLogLevel maps a config string to a slog.Level. Returns ok=false for the
// empty string (caller falls back to default) and for unrecognized values.
func parseLogLevel(s string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return 0, false
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	}
	return 0, false
}

// newLogger builds a text-handler logger at the effective level.
func (a *app) newLogger(out io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: a.resolveLogLevel()}))
}

// warnInvalidLogLevel prints a warning when cfg.LogLevel is set but unparseable.
// Returns true if a warning was emitted.
func (a *app) warnInvalidLogLevel(out io.Writer) bool {
	if a.cfg == nil || a.cfg.LogLevel == "" {
		return false
	}
	if _, ok := parseLogLevel(a.cfg.LogLevel); ok {
		return false
	}
	_, _ = fmt.Fprintf(out, "Warning: ignoring invalid log_level %q (expected debug|info|warn|error)\n", a.cfg.LogLevel)
	return true
}
