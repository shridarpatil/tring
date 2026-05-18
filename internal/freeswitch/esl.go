// Package freeswitch wraps the ESL operations the API needs. It is a thin
// translation layer — the API does not (and should not) reach into eslgo
// types directly.
package freeswitch

import (
	"context"
	"fmt"
	"strings"

	"github.com/percipia/eslgo"
	"github.com/percipia/eslgo/command"
)

type Client struct {
	addr     string
	password string
}

func New(addr, password string) *Client {
	return &Client{addr: addr, password: password}
}

// OriginateRequest is everything Tring needs to ask FreeSWITCH to place a
// call. v0 supports the two-leg pattern: ring ALeg first; on answer, bridge
// to BLeg. Vars are applied as global channel vars on the originate.
type OriginateRequest struct {
	ALeg    string            // e.g. "user/alice@t_dev.tring.local"
	BLeg    string            // e.g. "sofia/gateway/exotel/+919876543210"
	UUID    string            // pre-allocated origination_uuid we'll write to DB
	CallerID string           // becomes origination_caller_id_number
	Vars    map[string]string // extra channel vars (override caller-id etc.)
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

	// FreeSWITCH originate interprets the second argument as an *extension*
	// (or `&app(args)`), NOT as a B-leg URL. To actually bridge the A-leg
	// to a SIP/loopback/gateway URL, wrap it in `&bridge(...)`. Without
	// this, FS rings alice, then drops her into the dialplan with
	// destination_number = the literal B-leg string (e.g. "loopback/9196")
	// which matches nothing and falls through to the catch-all that
	// transfers to a non-existent context → hangup ~10s after answer.
	args := fmt.Sprintf("%s%s &bridge(%s)", buildVarBlock(vars), req.ALeg, req.BLeg)

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
