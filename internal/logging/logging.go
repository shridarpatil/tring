// Package logging builds the application's structured slog.Logger from config.
//
// Output is JSON or logfmt-style text via a swappable handler, so logs are
// ready to ship to a log pipeline (logchef, Loki, …) in prod (json) while
// staying readable in local dev (text). It's the one place log format/level
// are decided; binaries call New once in main and thread the *slog.Logger down.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a slog.Logger writing to stderr. format is "json" (for log
// shippers) or "text" (logfmt, friendlier locally); anything else is text.
// level is debug|info|warn|error, defaulting to info.
func New(format, level string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}
	var h slog.Handler
	if strings.EqualFold(format, "json") {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
