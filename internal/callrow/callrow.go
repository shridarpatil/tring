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
	// Disposition / Notes / WrappedUpAt / WrapUpEnd are the call's wrap-up
	// (migration 0039): null/omitted until the agent saves or the timer closes
	// it. WrapUpEnd is "done" | "timeout".
	Disposition *Disposition `json:"disposition"`
	Notes       *string      `json:"notes"`
	WrappedUpAt *time.Time   `json:"wrapped_up_at"`
	WrapUpEnd   *string      `json:"wrap_up_end"`
	// Tags are the call's wrap-up tags: always an array, never null. Filled by
	// FillTags (one query per list), not by Scan.
	Tags []Tag `json:"tags"`
	// QueueTeam / QueuedAt / QueueSLASeconds describe the team queue the call
	// is waiting in right now. Only list-active-calls fills them (ScanActive).
	QueueTeam       *string    `json:"queue_team,omitempty"`
	QueuedAt        *time.Time `json:"queued_at,omitempty"`
	QueueSLASeconds *int       `json:"queue_sla_seconds,omitempty"`
}

// Disposition is the {id, name} a wrapped-up call carries.
type Disposition struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// Tag is the {id, name} of a wrap-up tag on a call.
type Tag struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
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

// ScanWith reads a row of the Scan columns followed by query-specific ones
// into extra, for lists that add a column without widening Call (the webhook
// contract).
func ScanWith(scanner interface{ Scan(...interface{}) error }, extra ...interface{}) (Call, error) {
	var c Call
	return c, scan(scanner, &c, extra...)
}

func scan(scanner interface{ Scan(...interface{}) error }, c *Call, extra ...interface{}) error {
	// Scan the jsonb ivr_path into a plain []byte, then hand it to the
	// RawMessage field — pgx returns jsonb as raw bytes and we pass them
	// through to the client unchanged.
	var ivrPath []byte
	var dispID *uuid.UUID
	var dispName *string
	c.Tags = []Tag{}
	dest := append([]interface{}{
		&c.ID, &c.TenantID, &c.Direction, &c.Status, &c.FromNumber, &c.ToNumber,
		&c.AgentID, &c.LegID, &c.StartedAt, &c.AnsweredAt, &c.EndedAt,
		&c.DurationSec, &c.HangupCause, &c.RecordingURL, &ivrPath, &c.AgentName, &c.ClientRef, &c.Source,
		&c.RoomID, &dispID, &dispName, &c.Notes, &c.WrappedUpAt, &c.WrapUpEnd,
	}, extra...)
	err := scanner.Scan(dest...)
	if dispID != nil && dispName != nil {
		c.Disposition = &Disposition{ID: *dispID, Name: *dispName}
	}
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
	c, err := Scan(d.Pool.QueryRow(ctx, q.GetCall, id, tenantID))
	if err != nil {
		return c, err
	}
	calls := []Call{c}
	if err := FillTags(ctx, d, q, tenantID, calls); err != nil {
		return c, err
	}
	return calls[0], nil
}

// FillTags loads the tags of every call in calls with one query.
func FillTags(ctx context.Context, d *db.DB, q *queries.Queries, tenantID uuid.UUID, calls []Call) error {
	if len(calls) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(calls))
	at := make(map[uuid.UUID]int, len(calls))
	for i, c := range calls {
		ids[i] = c.ID
		at[c.ID] = i
	}
	rows, err := d.Pool.Query(ctx, q.ListCallTagsForCalls, tenantID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var callID uuid.UUID
		var t Tag
		if err := rows.Scan(&callID, &t.ID, &t.Name); err != nil {
			return err
		}
		i := at[callID]
		calls[i].Tags = append(calls[i].Tags, t)
	}
	return rows.Err()
}

// Payload is the `data` of a call.* webhook: {"call": <Call>}.
func Payload(c Call) map[string]interface{} {
	return map[string]interface{}{"call": c}
}
