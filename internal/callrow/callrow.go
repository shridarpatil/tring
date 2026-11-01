// Package callrow is the one definition of a call-log row as Tring exposes it:
// the JSON the API returns for a call AND the `call` object every call.*
// webhook carries. The api (call.ringing, GET /v1/calls) and the gateway's
// call log (call.answered, call.ended) both build it from here, so the webhook
// contract can't drift between the two binaries.
package callrow

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/db"
	"github.com/shridarpatil/tring/internal/queries"
)

// Call is a calls-table row. Column order matches Scan and the get-call /
// list-calls queries.
type Call struct {
	ID           uuid.UUID  `json:"id"`
	TenantID     uuid.UUID  `json:"tenant_id"`
	Direction    string     `json:"direction"`
	Status       string     `json:"status"`
	FromNumber   string     `json:"from_number"`
	ToNumber     string     `json:"to_number"`
	AgentID      *uuid.UUID `json:"agent_id,omitempty"`
	AgentName    *string    `json:"agent_name,omitempty"` // display_name|sip of the handling agent
	ClientRef    *string    `json:"client_ref,omitempty"` // embedder-supplied caller identity (vs guest_*)
	Source       string     `json:"source"`               // call channel: web | phone | whatsapp
	LegID        string     `json:"leg_id"`               // opaque per-call leg id (FS channel UUID or pion leg)
	StartedAt    time.Time  `json:"started_at"`
	AnsweredAt   *time.Time `json:"answered_at,omitempty"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	DurationSec  *int       `json:"duration_seconds,omitempty"`
	HangupCause  *string    `json:"hangup_cause,omitempty"`
	RecordingURL *string    `json:"recording_url,omitempty"`
	// IVRPath is the recorded IVR journey for inbound/IVR calls — an ordered
	// JSON array of steps (node, DTMF pressed, transfer target, outcome).
	// Passed through verbatim from the calls.ivr_path JSONB column; empty
	// array for outbound calls. Shape: see brain.SimStep / the
	// console's IvrStep type.
	IVRPath json.RawMessage `json:"ivr_path,omitempty"`
	// RoomID is the pion room id ("<tenant>/<name>") for media-plane calls —
	// lets the dashboard match a call.supervised event to its row. Empty for
	// FreeSWITCH-only calls.
	RoomID string `json:"room_id,omitempty"`
	// QueueTeam / QueuedAt / QueueSLASeconds describe the team queue the call
	// is waiting in right now. Only list-active-calls fills them (ScanActive).
	QueueTeam       *string    `json:"queue_team,omitempty"`
	QueuedAt        *time.Time `json:"queued_at,omitempty"`
	QueueSLASeconds *int       `json:"queue_sla_seconds,omitempty"`
}

// Scan reads one row in the get-call / list-calls column order.
func Scan(scanner interface{ Scan(...interface{}) error }) (Call, error) {
	var c Call
	return c, scan(scanner, &c)
}

// ScanActive reads a list-active-calls row: the Scan columns followed by the
// call's open queue stint (team, enqueued_at, sla_seconds), NULL when none.
func ScanActive(scanner interface{ Scan(...interface{}) error }) (Call, error) {
	var c Call
	return c, scan(scanner, &c, &c.QueueTeam, &c.QueuedAt, &c.QueueSLASeconds)
}

func scan(scanner interface{ Scan(...interface{}) error }, c *Call, extra ...interface{}) error {
	// Scan the jsonb ivr_path into a plain []byte, then hand it to the
	// RawMessage field — pgx returns jsonb as raw bytes and we pass them
	// through to the client unchanged.
	var ivrPath []byte
	dest := append([]interface{}{
		&c.ID, &c.TenantID, &c.Direction, &c.Status, &c.FromNumber, &c.ToNumber,
		&c.AgentID, &c.LegID, &c.StartedAt, &c.AnsweredAt, &c.EndedAt,
		&c.DurationSec, &c.HangupCause, &c.RecordingURL, &ivrPath, &c.AgentName, &c.ClientRef, &c.Source,
		&c.RoomID,
	}, extra...)
	err := scanner.Scan(dest...)
	if len(ivrPath) > 0 {
		c.IVRPath = ivrPath
	}
	return err
}

// RecordingCacheKey is the Redis key for a tenant's recording settings
// (get-tenant-record-calls), which the gateway's call log reads on every call.
// Here so the api can invalidate it on a settings change without importing the
// media plane.
func RecordingCacheKey(tenantID uuid.UUID) string { return "tenant:recording:" + tenantID.String() }

// Get loads one of the tenant's calls by id.
func Get(ctx context.Context, d *db.DB, q *queries.Queries, tenantID, id uuid.UUID) (Call, error) {
	return Scan(d.Pool.QueryRow(ctx, q.GetCall, id, tenantID))
}

// Payload is the `data` of a call.* webhook: {"call": <Call>}.
func Payload(c Call) map[string]interface{} {
	return map[string]interface{}{"call": c}
}
