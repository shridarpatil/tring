// Package freeswitch wraps the ESL operations the API needs. It is a thin
// translation layer — the API does not (and should not) reach into eslgo
// types directly.
package freeswitch

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/percipia/eslgo"
	"github.com/percipia/eslgo/command"
)

type Client struct {
	addr     string
	password string
}

func New(addr, password string) *Client {
	// We open a fresh ESL connection per originate (see Originate), so eslgo's
	// NormalLogger emits an "auth ok" + "Error receiving message: use of closed
	// network connection" pair on every call — pure teardown noise. Swap in a
	// filtering logger that drops that chatter but still surfaces real
	// warnings/errors. DefaultOptions.Logger is eslgo's sanctioned config hook.
	eslgo.DefaultOptions.Logger = quietLogger{}
	return &Client{addr: addr, password: password}
}

// quietLogger implements eslgo.Logger, suppressing the benign connect/teardown
// messages from the fresh-connection-per-originate pattern while still
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
	log.Printf("esl warn: %s", strings.TrimSpace(msg))
}

func (quietLogger) Error(format string, args ...any) {
	log.Printf("esl error: %s", strings.TrimSpace(fmt.Sprintf(format, args...)))
}

// OriginateRequest is everything Tring needs to ask FreeSWITCH to place a
// call. ALeg rings first; on answer, FS bridges to BLegs (joined with `|`
// for failover). Each entry in BLegs is already a full dial string,
// optionally prefixed with `[var=val,...]` per-leg variables — typically
// `effective_caller_id_number=<that-trunk's-DID>` so each trunk presents
// its own allocated DID, not the primary's.
//
// Vars become global `{k=v,...}` channel variables on the A-leg. Most
// important: `continue_on_fail` controls which hangup causes roll over to
// the next BLeg.
type OriginateRequest struct {
	ALeg     string
	BLegs    []string // ≥1 entry; if >1 they're pipe-joined inside &bridge()
	UUID     string
	CallerID string
	Vars     map[string]string
}

// OriginateResult holds the two FreeSWITCH identifiers tied to a single
// originate request: the pre-allocated channel UUID we set (== req.UUID),
// and the bgapi Job-UUID returned in the reply. The orchestrator uses the
// job UUID to catch failed-to-create-channel originates via BACKGROUND_JOB
// events (no channel = no CHANNEL_HANGUP_COMPLETE, so the job event is the
// only signal we get).
type OriginateResult struct {
	ChannelUUID string
	JobUUID     string
}

// Originate places a backgrounded originate. v0 opens a fresh ESL connection
// per call — fine at low concurrency; Phase 2's orchestrator will hold a
// persistent connection.
func (c *Client) Originate(ctx context.Context, req OriginateRequest) (OriginateResult, error) {
	conn, err := eslgo.Dial(c.addr, c.password, func() {})
	if err != nil {
		return OriginateResult{}, fmt.Errorf("esl dial: %w", err)
	}
	defer conn.Close()

	vars := map[string]string{}
	for k, v := range req.Vars {
		vars[k] = v
	}
	if req.UUID != "" {
		vars["origination_uuid"] = req.UUID
	}
	if req.CallerID != "" {
		vars["origination_caller_id_number"] = req.CallerID
	}

	if len(req.BLegs) == 0 {
		return OriginateResult{}, fmt.Errorf("originate: no B-legs")
	}
	// FreeSWITCH originate interprets the second argument as an *extension*
	// (or `&app(args)`), NOT as a B-leg URL. To actually bridge to a
	// SIP/loopback/gateway URL, wrap it in `&bridge(...)`. Multiple
	// pipe-separated dial strings inside `&bridge()` are tried in order
	// for failover; `continue_on_fail` (set on the A-leg vars) constrains
	// which causes trigger the rollover.
	bridgeArgs := strings.Join(req.BLegs, "|")
	args := fmt.Sprintf("%s%s &bridge(%s)", buildVarBlock(vars), req.ALeg, bridgeArgs)

	resp, err := conn.SendCommand(ctx, command.API{
		Command:    "originate",
		Arguments:  args,
		Background: true,
	})
	if err != nil {
		return OriginateResult{}, fmt.Errorf("send originate: %w", err)
	}

	// For bgapi, the result lands in Reply-Text (e.g.
	// "+OK Job-UUID: 7a87..."). For sync api, it lands in the body.
	// GetReply prefers the header but falls back to the body.
	reply := strings.TrimSpace(resp.GetReply())
	if strings.HasPrefix(reply, "-ERR") {
		return OriginateResult{}, fmt.Errorf("freeswitch rejected originate: %s", reply)
	}
	return OriginateResult{
		ChannelUUID: req.UUID,
		JobUUID:     parseJobUUID(reply),
	}, nil
}

// parseJobUUID extracts the job-uuid from a bgapi reply like:
//
//	+OK Job-UUID: 7a873c14-4fac-4957-97cd-567916b72d8b
func parseJobUUID(body string) string {
	const tag = "Job-UUID:"
	idx := strings.Index(body, tag)
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(body[idx+len(tag):])
}

// buildVarBlock formats `{k1=v1,k2=v2}` for FreeSWITCH originate. Returns
// "" if the map is empty so callers can concatenate safely.
func buildVarBlock(vars map[string]string) string {
	if len(vars) == 0 {
		return ""
	}
	parts := make([]string, 0, len(vars))
	for k, v := range vars {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}
