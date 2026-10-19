package freeswitch

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/percipia/eslgo"
)

func TestESLLoggerDropsOwnCloseKeepsRealErrors(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	if _, ok := eslgo.DefaultInboundOptions.Logger.(eslLogger); !ok {
		t.Fatalf("eslgo inbound logger = %T, want eslLogger", eslgo.DefaultInboundOptions.Logger)
	}
	l := eslLogger{}
	l.Warn("Error receiving message: %s\n", "read tcp 127.0.0.1:58266->127.0.0.1:8021: use of closed network connection")
	if buf.Len() != 0 {
		t.Fatalf("closed-connection message was logged: %q", buf.String())
	}

	l.Warn("Error receiving message: %s\n", "read tcp 127.0.0.1:58266->127.0.0.1:8021: connection reset by peer")
	out := buf.String()
	for _, want := range []string{"level=WARN", "connection reset by peer", "component=esl"} {
		if !strings.Contains(out, want) {
			t.Errorf("real ESL error not logged as expected: missing %q in %q", want, out)
		}
	}
}
