package callcdr_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pion/rtp"

	"github.com/whatomate/tring/internal/callcdr"
	"github.com/whatomate/tring/internal/dbtest"
	"github.com/whatomate/tring/internal/ivr"
)

func opusPkt(ts uint32) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 111, Timestamp: ts},
		Payload: []byte{0x78, 0x01, 0x02},
	}
}

// TestLifecycleWritesCDRAndRecording covers the whole arc for a recorded call:
// Started (ringing row + recording_url) → media on both legs → Answered →
// Journey → Ended (settled row + finalized stereo file in the spool).
func TestLifecycleWritesCDRAndRecording(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	if _, err := fx.DB.Pool.Exec(ctx, `UPDATE tenants SET record_calls = true WHERE id = $1`, fx.TenantID); err != nil {
		t.Fatalf("enable recording: %v", err)
	}
	dir := t.TempDir()
	l := callcdr.New(fx.DB, nil, dir, nil)

	roomID := fx.TenantID.String() + "/pstn-test"
	sink := l.Started(ctx, fx.TenantID, roomID, "+919900112233", "16001122", "", "caller-pid")
	if sink == nil {
		t.Fatal("expected a recording sink (tenant records)")
	}
	for i := range 20 {
		_ = sink.WriteRTP("caller-pid", opusPkt(uint32(i*960)))
	}

	agentSink := l.Answered(roomID, "agent-pid")
	if agentSink == nil {
		t.Fatal("expected the agent recording sink")
	}
	if l.Answered(roomID, "caller-pid") != nil {
		t.Fatal("the caller's own join must not mark answered")
	}
	for i := range 20 {
		_ = agentSink.WriteRTP("agent-pid", opusPkt(uint32(i*960)))
	}

	l.Journey(roomID, []ivr.Step{{NodeID: "g", NodeType: "greeting", Action: "play_audio", Outcome: "default"}})
	l.Ended(roomID, "caller_hangup")
	l.Ended(roomID, "again") // idempotent

	// Row settled?
	var (
		legID, status, cause, recURL string
		duration                     int
		answeredOK                   bool
		journeyLen                   int
	)
	err := fx.DB.Pool.QueryRow(ctx, `
		SELECT fs_uuid, status::text, hangup_cause, COALESCE(recording_url,''),
		       COALESCE(duration_seconds, -1), answered_at IS NOT NULL,
		       jsonb_array_length(ivr_path)
		FROM calls WHERE tenant_id = $1`, fx.TenantID).
		Scan(&legID, &status, &cause, &recURL, &duration, &answeredOK, &journeyLen)
	if err != nil {
		t.Fatalf("read call row: %v", err)
	}
	if status != "ended" || cause != "caller_hangup" || !answeredOK || duration < 0 {
		t.Fatalf("row = status %s cause %s answered %v dur %d", status, cause, answeredOK, duration)
	}
	if recURL == "" || journeyLen != 1 {
		t.Fatalf("recording_url %q journey %d", recURL, journeyLen)
	}

	// Recording finalized (async) → <dir>/<legID>.ogg with the OggS magic.
	final := filepath.Join(dir, legID+".ogg")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(final); err == nil && len(b) > 4 && string(b[:4]) == "OggS" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("finalized recording %s never appeared", final)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestNoRecordingWhenTenantOff: CDR still written, but no sink and no file.
// (record_calls defaults to true in the schema, so disable it explicitly.)
func TestNoRecordingWhenTenantOff(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	if _, err := fx.DB.Pool.Exec(ctx, `UPDATE tenants SET record_calls = false WHERE id = $1`, fx.TenantID); err != nil {
		t.Fatalf("disable recording: %v", err)
	}
	dir := t.TempDir()
	l := callcdr.New(fx.DB, nil, dir, nil)

	roomID := fx.TenantID.String() + "/web-x"
	if sink := l.Started(ctx, fx.TenantID, roomID, "visitor", "web", "cust_42", "p1"); sink != nil {
		t.Fatal("tenant has record_calls off — no sink expected")
	}
	l.Ended(roomID, "ended")

	var status, ref string
	err := fx.DB.Pool.QueryRow(ctx,
		`SELECT status::text, COALESCE(client_ref,'') FROM calls WHERE tenant_id = $1`, fx.TenantID).
		Scan(&status, &ref)
	if err != nil {
		t.Fatalf("read row: %v", err)
	}
	if status != "ended" || ref != "cust_42" {
		t.Fatalf("status %s ref %q", status, ref)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("unexpected files in spool: %v", entries)
	}
}
