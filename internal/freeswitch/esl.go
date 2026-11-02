// Package freeswitch wraps the ESL operations the API needs. It is a thin
// translation layer — the API does not (and should not) reach into eslgo
// types directly.
package freeswitch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/percipia/eslgo"
	"github.com/percipia/eslgo/command"
)

// ErrUnsafeArg is returned, before anything is sent, when a command argument
// contains a control character. ESL is line-framed: a newline would end the
// command and start a second one with full ESL privileges. Callers validate
// their inputs; this is the backstop.
var ErrUnsafeArg = errors.New("freeswitch: control character in command argument")

// checkArgs returns ErrUnsafeArg if any argument has a control character.
func checkArgs(args ...string) error {
	for _, a := range args {
		if strings.IndexFunc(a, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return ErrUnsafeArg
		}
	}
	return nil
}

type Client struct {
	addr     string
	password string
}

func New(addr, password string) *Client {
	// We open a fresh ESL connection per command (see Command), so eslgo's
	// NormalLogger emits an "auth ok" + "Error receiving message: use of closed
	// network connection" pair on every call — pure teardown noise. Swap in a
	// filtering logger that drops that chatter but still surfaces real
	// warnings/errors. DefaultOptions.Logger is eslgo's sanctioned config hook.
	eslgo.DefaultOptions.Logger = quietLogger{}
	return &Client{addr: addr, password: password}
}

// quietLogger implements eslgo.Logger, suppressing the benign connect/teardown
// messages from the fresh-connection-per-command pattern while still
// surfacing genuine warnings and errors.
type quietLogger struct{}

func (quietLogger) Debug(string, ...any) {}
func (quietLogger) Info(string, ...any)  {} // per-connection "Successfully authenticated"

func (quietLogger) Warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "Connection closed, stopping receive loop") {
		return // expected when we Close() a short-lived originate connection
	}
	slog.Warn("eslgo warning", "component", "freeswitch", "msg", strings.TrimSpace(msg))
}

func (quietLogger) Error(format string, args ...any) {
	slog.Error("eslgo error", "component", "freeswitch", "msg", strings.TrimSpace(fmt.Sprintf(format, args...)))
}

// Command runs a synchronous FreeSWITCH API command on a fresh ESL connection
// and returns the reply. Used for call-control actions on an existing channel —
// e.g. Command(ctx, "uuid_hold", "<uuid>") to hold, "uuid_hold", "off <uuid>" to
// resume, "uuid_transfer", "<uuid> <dest>" to transfer. Returns an error if FS
// replies -ERR (e.g. the channel no longer exists).
func (c *Client) Command(ctx context.Context, cmd, args string) (string, error) {
	if err := checkArgs(cmd, args); err != nil {
		return "", err
	}
	conn, err := eslgo.Dial(c.addr, c.password, func() {})
	if err != nil {
		return "", fmt.Errorf("esl dial: %w", err)
	}
	defer conn.Close()

	resp, err := conn.SendCommand(ctx, command.API{Command: cmd, Arguments: args})
	if err != nil {
		return "", fmt.Errorf("send %s: %w", cmd, err)
	}
	reply := strings.TrimSpace(resp.GetReply())
	if strings.HasPrefix(reply, "-ERR") || strings.HasPrefix(reply, "-USAGE") {
		return "", fmt.Errorf("freeswitch %s rejected: %s", cmd, reply)
	}
	return reply, nil
}
