package callcdr_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pion/rtp"

	"github.com/shridarpatil/tring/internal/callcdr"
	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/ivr"
	"github.com/shridarpatil/tring/internal/logging"
)

// routingSink captures the last call.routing event's data, for asserting what the
// live dashboard would show as the ring target.
type routingSink struct {
	mu   sync.Mutex
	last map[string]any
}

func (s *routingSink) Publish(_ context.Context, _, typ string, data any) {
	if typ != "call.routing" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := data.(map[string]any); ok {
		s.last = m
	}
}

func (s *routingSink) lastTarget() any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		return nil
	}
	return s.last["target"]
}

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
	var agentID uuid.UUID
	if err := fx.DB.Pool.QueryRow(ctx,
		`INSERT INTO agents (tenant_id, sip_username, sip_password) VALUES ($1, 'alice', 'x') RETURNING id`,
		fx.TenantID).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	dir := t.TempDir()
	l := callcdr.New(fx.DB, nil, dir, nil, nil)

	roomID := fx.TenantID.String() + "/pstn-test"
	sink := l.Started(ctx, fx.TenantID, roomID, "+919900112233", "16001122", "phone", "", "caller-pid")
	if sink == nil {
		t.Fatal("expected a recording sink (tenant records)")
	}
	// Pre-answer caller audio (IVR/ringing) is DROPPED — capture starts at answer.
	for i := range 20 {
		_ = sink.WriteRTP("caller-pid", opusPkt(uint32(i*960)))
	}

	agentSink := l.Answered(roomID, "agent-pid", "alice") // arms the recorder
	if agentSink == nil {
		t.Fatal("expected the agent recording sink")
	}
	if l.Answered(roomID, "caller-pid", "alice") != nil {
		t.Fatal("the caller's own join must not mark answered")
	}
	// Post-answer: both legs speak — these are what land in the recording.
	for i := range 20 {
		_ = sink.WriteRTP("caller-pid", opusPkt(uint32((20+i)*960)))
		_ = agentSink.WriteRTP("agent-pid", opusPkt(uint32((20+i)*960)))
	}

	l.Journey(roomID, []ivr.Step{{NodeID: "g", NodeType: "greeting", Action: "play_audio", Outcome: "default"}})
	l.Ended(roomID, "caller_hangup")
	l.Ended(roomID, "again") // idempotent

	// Row settled? (recording_url is set asynchronously by finalize — checked
	// after the file appears, below.)
	var (
		legID, status, cause, source string
		duration                     int
		answeredOK                   bool
		journeyLen                   int
		gotAgent                     *uuid.UUID
	)
	err := fx.DB.Pool.QueryRow(ctx, `
		SELECT leg_id, status::text, hangup_cause,
		       COALESCE(duration_seconds, -1), answered_at IS NOT NULL,
		       jsonb_array_length(ivr_path), agent_id, source::text
		FROM calls WHERE tenant_id = $1`, fx.TenantID).
		Scan(&legID, &status, &cause, &duration, &answeredOK, &journeyLen, &gotAgent, &source)
	if err != nil {
		t.Fatalf("read call row: %v", err)
	}
	if status != "ended" || cause != "caller_hangup" || !answeredOK || duration < 0 {
		t.Fatalf("row = status %s cause %s answered %v dur %d", status, cause, answeredOK, duration)
	}
	if source != "phone" {
		t.Fatalf("source = %s, want phone", source)
	}
	if journeyLen != 1 {
		t.Fatalf("journey %d", journeyLen)
	}
	if gotAgent == nil || *gotAgent != agentID {
		t.Fatalf("agent_id = %v, want %v (answering agent not attributed)", gotAgent, agentID)
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

	// recording_url is published ONLY once the file exists (so a no-media call
	// never advertises a dead recording). Poll briefly — set just after the file.
	deadline = time.Now().Add(2 * time.Second)
	for {
		var recURL string
		if err := fx.DB.Pool.QueryRow(ctx,
			`SELECT COALESCE(recording_url,'') FROM calls WHERE leg_id = $1`, legID).Scan(&recURL); err != nil {
			t.Fatalf("read recording_url: %v", err)
		} else if recURL != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recording_url never set after the file was finalized")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestNoMediaLeavesNoRecordingURL is the regression for the "recording not
// available yet" bug: a recorded call that captures NO audio (silent caller /
// IVR-only / no-answer) must NOT advertise a recording_url, so the UI shows no
// dead play button.
func TestNoMediaLeavesNoRecordingURL(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	if _, err := fx.DB.Pool.Exec(ctx, `UPDATE tenants SET record_calls = true WHERE id = $1`, fx.TenantID); err != nil {
		t.Fatalf("enable recording: %v", err)
	}
	dir := t.TempDir()
	l := callcdr.New(fx.DB, nil, dir, nil, nil)

	roomID := fx.TenantID.String() + "/silent"
	if sink := l.Started(ctx, fx.TenantID, roomID, "+9199", "16001122", "phone", "", "caller-pid"); sink == nil {
		t.Fatal("expected a recording sink (tenant records)")
	}
	// No WriteRTP — the caller never spoke. End the call.
	l.Ended(roomID, "no_answer")

	// Give the async finalize time to run, then assert NO url and NO file.
	time.Sleep(500 * time.Millisecond)
	var recURL string
	if err := fx.DB.Pool.QueryRow(ctx,
		`SELECT COALESCE(recording_url,'') FROM calls WHERE tenant_id = $1`, fx.TenantID).Scan(&recURL); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if recURL != "" {
		t.Fatalf("no media captured but recording_url = %q (dead play button)", recURL)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("no media but spool has files: %v", entries)
	}
}

// TestStartedOutboundRecordsOutboundDirection is the regression guard for the
// agent-initiated-outbound mislabeling bug: StartedOutbound (unlike Started,
// which always writes 'inbound') must land a `calls` row with
// direction='outbound' and source='phone', and the live-dashboard ringingEvent
// it publishes must say "outbound" too — not the "inbound"/"phone" tuple every
// call used to get regardless of who initiated it. It also guards the
// supervisor-routing regression (Task 4d): the row's room_id must be
// persisted — get-supervise-target reads room_id, so a NULL here silently
// made agent-initiated outbound calls unsupervisable.
func TestStartedOutboundRecordsOutboundDirection(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	if _, err := fx.DB.Pool.Exec(ctx, `UPDATE tenants SET record_calls = false WHERE id = $1`, fx.TenantID); err != nil {
		t.Fatalf("disable recording: %v", err)
	}
	dir := t.TempDir()
	l := callcdr.New(fx.DB, nil, dir, nil, nil)

	roomID := fx.TenantID.String() + "/agent-dial"
	sink := l.StartedOutbound(ctx, fx.TenantID, roomID, "16001122", "+919900112233", "", "caller-pid")
	if sink != nil {
		t.Fatal("tenant has record_calls off — no sink expected")
	}
	l.Ended(roomID, "caller_hangup")

	var direction, source, gotRoomID string
	err := fx.DB.Pool.QueryRow(ctx,
		`SELECT direction::text, source::text, COALESCE(room_id, '') FROM calls WHERE tenant_id = $1`, fx.TenantID).
		Scan(&direction, &source, &gotRoomID)
	if err != nil {
		t.Fatalf("read call row: %v", err)
	}
	if direction != "outbound" {
		t.Fatalf("direction = %q, want outbound", direction)
	}
	if source != "phone" {
		t.Fatalf("source = %q, want phone", source)
	}
	if gotRoomID != roomID {
		t.Fatalf("room_id = %q, want %q (supervisor routing needs this persisted)", gotRoomID, roomID)
	}
}

// TestTimelineEvents covers the durable per-call history (call_events): the full
// routing arc — started → ring → no_answer → ring → answered → transfer → ended —
// lands as ordered, typed rows with resolved detail (team name, agent name).
func TestTimelineEvents(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	if _, err := fx.DB.Pool.Exec(ctx,
		`INSERT INTO agents (tenant_id, sip_username, sip_password, display_name) VALUES ($1,'alice','x','Alice A')`,
		fx.TenantID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	l := callcdr.New(fx.DB, nil, t.TempDir(), nil, nil)
	roomID := fx.TenantID.String() + "/tl-test"

	l.Started(ctx, fx.TenantID, roomID, "+15551230000", "Support flow", "web", "", "caller-pid")
	l.RingStart(ctx, roomID, "Sales", "")              // team window opens (live label only)
	l.RingAgent(roomID, "alice")                       // Alice is actually rung → ring(Alice A)
	l.RingStop(roomID)                                 // window ends unanswered → no_answer([Alice A])
	l.RingStart(ctx, roomID, "", "alice")              // direct-agent window
	l.RingAgent(roomID, "alice")                       // ring(Alice A)
	l.Answered(roomID, "agent-pid", "alice")           // answered by Alice
	l.Transferred(roomID, "alice", "Bob") // agent blind-transfers onward
	l.Ended(roomID, "caller_hangup")

	// agent := detail->>'agent' (ring/answered); noAns := first name in detail->'agents'
	// (no_answer); to := detail->>'to' (transfer).
	rows, err := fx.DB.Pool.Query(ctx,
		`SELECT type, COALESCE(detail->>'agent',''), COALESCE(detail->'agents'->>0,''), COALESCE(detail->>'to','')
		 FROM call_events WHERE tenant_id = $1 ORDER BY seq ASC`, fx.TenantID)
	if err != nil {
		t.Fatalf("read call_events: %v", err)
	}
	defer rows.Close()
	var types, agents, noAns, tos []string
	for rows.Next() {
		var typ, agent, na, to string
		if err := rows.Scan(&typ, &agent, &na, &to); err != nil {
			t.Fatalf("scan: %v", err)
		}
		types = append(types, typ)
		agents = append(agents, agent)
		noAns = append(noAns, na)
		tos = append(tos, to)
	}
	want := []string{"started", "ring", "no_answer", "ring", "answered", "transfer", "ended"}
	if len(types) != len(want) {
		t.Fatalf("timeline = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("event %d = %q, want %q (full: %v)", i, types[i], want[i], types)
		}
	}
	// ring events name the agent, not the team.
	if agents[1] != "Alice A" || agents[3] != "Alice A" {
		t.Fatalf("ring agent = %q/%q, want resolved display name Alice A", agents[1], agents[3])
	}
	if noAns[2] != "Alice A" {
		t.Fatalf("no_answer agents[0] = %q, want Alice A", noAns[2])
	}
	if agents[4] != "Alice A" {
		t.Fatalf("answered agent = %q, want Alice A", agents[4])
	}
	if tos[5] != "Bob" {
		t.Fatalf("transfer to = %q, want Bob", tos[5])
	}
}

// TestRingAgentLiveShowsAgentName is the regression for "live calls show the team
// name": once an agent is actually rung, the live dashboard ring target must be
// that agent's display name, overriding the team label RingStart set.
func TestRingAgentLiveShowsAgentName(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	if _, err := fx.DB.Pool.Exec(ctx,
		`INSERT INTO agents (tenant_id, sip_username, sip_password, display_name) VALUES ($1,'alice','x','Alice A')`,
		fx.TenantID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	sink := &routingSink{}
	l := callcdr.New(fx.DB, nil, t.TempDir(), nil, sink)
	roomID := fx.TenantID.String() + "/live-test"

	l.Started(ctx, fx.TenantID, roomID, "visitor", "Support flow", "web", "", "caller-pid")
	l.RingStart(ctx, roomID, "Sales", "") // team label published first
	if got := sink.lastTarget(); got != "Sales" {
		t.Fatalf("pre-ring live target = %v, want the team label Sales", got)
	}
	l.RingAgent(roomID, "alice") // now an agent is actually ringing
	if got := sink.lastTarget(); got != "Alice A" {
		t.Fatalf("live ring target = %v, want the agent name Alice A (not the team)", got)
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
	l := callcdr.New(fx.DB, nil, dir, nil, nil)

	roomID := fx.TenantID.String() + "/web-x"
	if sink := l.Started(ctx, fx.TenantID, roomID, "visitor", "Support", "web", "cust_42", "p1"); sink != nil {
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

// TestLogLinesCarryCallID: while a call is live, any log line naming its room
// (from signaling, the IVR, the SFU, …) gets call=<id>; once it ends, not.
func TestLogLinesCarryCallID(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	if _, err := fx.DB.Pool.Exec(ctx, `UPDATE tenants SET record_calls = false WHERE id = $1`, fx.TenantID); err != nil {
		t.Fatalf("disable recording: %v", err)
	}
	var buf bytes.Buffer
	log := slog.New(logging.WithCallIDs(slog.NewTextHandler(&buf, nil)))
	l := callcdr.New(fx.DB, nil, t.TempDir(), nil, nil)

	roomID := fx.TenantID.String() + "/pstn-logs"
	l.Started(ctx, fx.TenantID, roomID, "+919800000001", "+919800000002", "phone", "", "caller-pid")
	callID := l.CallID(roomID)
	log.Info("ivr started", "room", roomID)
	l.Ended(roomID, "caller_hangup")
	log.Info("pstn: room ended", "room", roomID)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if want := "call=" + callID.String(); !strings.Contains(lines[0], want) {
		t.Errorf("live call line %q lacks %s", lines[0], want)
	}
	if strings.Contains(lines[1], "call=") {
		t.Errorf("line after the call ended still tagged: %q", lines[1])
	}
}
