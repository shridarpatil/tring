package freeswitch

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/percipia/eslgo"
)

// eslgo logs through Go's std log by default. Route it into slog instead, and
// drop the one message that isn't news: every ESL helper here dials, sends a
// command and closes, and on that close eslgo's reader goroutine reports
// "Error receiving message: … use of closed network connection" — once per
// call, i.e. every few seconds while the console polls trunk status.
func init() { eslgo.DefaultInboundOptions.Logger = eslLogger{} }

type eslLogger struct{}

func (eslLogger) Debug(format string, args ...interface{}) { eslLog(slog.LevelDebug, format, args) }
func (eslLogger) Info(format string, args ...interface{})  { eslLog(slog.LevelInfo, format, args) }
func (eslLogger) Warn(format string, args ...interface{})  { eslLog(slog.LevelWarn, format, args) }
func (eslLogger) Error(format string, args ...interface{}) { eslLog(slog.LevelError, format, args) }

func eslLog(level slog.Level, format string, args []interface{}) {
	msg := strings.TrimSpace(fmt.Sprintf(format, args...))
	if strings.Contains(msg, "use of closed network connection") {
		return // our own Close(); expected
	}
	slog.Default().Log(context.Background(), level, msg, "component", "esl")
}
