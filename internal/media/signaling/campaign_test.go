package signaling

import (
	"context"
	"errors"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pion/rtp"
	"github.com/shridarpatil/tring/internal/campaign"
	"github.com/shridarpatil/tring/internal/ivr"
	"github.com/shridarpatil/tring/internal/media/room"
)

type campaignMemory struct {
	mu       sync.Mutex
	a        campaign.Attempt
	claimed  bool
	answered bool
	done     chan string
}

func (m *campaignMemory) Claim(_ context.Context, _, _ uuid.UUID) (campaign.Attempt, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimed {
		return m.a, false, nil
	}
	m.claimed = true
	return m.a, true, nil
}
func (m *campaignMemory) SetCall(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error { return nil }
func (m *campaignMemory) Answered(context.Context, uuid.UUID, uuid.UUID) error {
	m.mu.Lock()
	m.answered = true
	m.mu.Unlock()
	return nil
}
func (m *campaignMemory) Heartbeat(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (m *campaignMemory) Finish(_ context.Context, _, _ uuid.UUID, status, reason string) error {
	m.done <- status + ":" + reason
	return nil
}
func campaignAudioFixture(t *testing.T, seconds string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg required")
	}
	path := filepath.Join(t.TempDir(), "message.ogg")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", seconds, "-c:a", "libopus", "-frame_duration", "20", path).CombinedOutput(); err != nil {
		t.Fatalf("audio fixture: %v %s", err, out)
	}
	return path
}
func campaignFixture(t *testing.T, leg connectLeg, audio string) (*Server, *campaignMemory, *connectOriginator) {
	t.Helper()
	o := newConnectOriginator(leg)
	cl := newConnectCallLog()
	s := newConnectServer(t, o, cl)
	trunks := s.cfg.Trunks.(fakeTrunkResolver)
	trunks.owned = map[string]bool{"+912265915000": true}
	s.cfg.Trunks = trunks
	m := &campaignMemory{done: make(chan string, 1), a: campaign.Attempt{ID: uuid.New(), CampaignID: uuid.New(), Phone: "+919876543210", Payload: campaign.Payload{Config: campaign.Config{CallerID: "+912265915000", RingSeconds: 5, MaxSeconds: 10}, Prepared: campaign.Prepared{AudioFile: audio}}}}
	s.cfg.Campaigns = m
	s.cfg.AudioDir = filepath.Dir(audio)
	return s, m, o
}
func TestCampaignPlaysOnlyAfterAnswerAndCloses(t *testing.T) {
	audio := campaignAudioFixture(t, "0.1")
	leg := newConnectLeg("campaign-customer")
	var mu sync.Mutex
	packets := 0
	leg.leg.Write = func(*rtp.Packet) error { mu.Lock(); packets++; mu.Unlock(); return nil }
	s, m, _ := campaignFixture(t, leg, audio)
	s.startPortCall()
	s.runCampaign(uuid.New(), m.a)
	if got := <-m.done; got != "completed:" {
		t.Fatal(got)
	}
	m.mu.Lock()
	answered := m.answered
	m.mu.Unlock()
	if !answered {
		t.Fatal("missing answer event")
	}
	mu.Lock()
	n := packets
	mu.Unlock()
	if n == 0 {
		t.Fatal("no audio reached the recipient")
	}
	if s.portCalls.Load() != 0 {
		t.Fatal("call leaked drain slot")
	}
	if got := waitEnd(t, s.cfg.Calls.(*connectCallLog)); got != "ended:campaign_completed" {
		t.Fatal(got)
	}
}
func TestCampaignEarlyHangupIsIncomplete(t *testing.T) {
	audio := campaignAudioFixture(t, "2")
	leg := newConnectLeg("campaign-customer")
	s, m, _ := campaignFixture(t, leg, audio)
	first := make(chan struct{}, 1)
	leg.leg.Write = func(*rtp.Packet) error {
		select {
		case first <- struct{}{}:
		default:
		}
		return nil
	}
	s.startPortCall()
	go s.runCampaign(uuid.New(), m.a)
	select {
	case <-first:
	case <-time.After(3 * time.Second):
		t.Fatal("no playback")
	}
	close(leg.done)
	select {
	case got := <-m.done:
		if !strings.HasPrefix(got, "incomplete:") {
			t.Fatal(got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("call did not end")
	}
}
func TestCampaignInvalidAudioNeverDials(t *testing.T) {
	s, m, o := campaignFixture(t, newConnectLeg("customer"), filepath.Join(t.TempDir(), "missing.ogg"))
	s.startPortCall()
	s.runCampaign(uuid.New(), m.a)
	if got := <-m.done; got != "failed:invalid_audio" {
		t.Fatal(got)
	}
	o.mu.Lock()
	n := o.n
	o.mu.Unlock()
	if n != 0 {
		t.Fatal("dialed without playable audio")
	}
}
func TestCampaignBusyDoesNotReportAnswered(t *testing.T) {
	s, m, _ := campaignFixture(t, connectLeg{err: &room.DialFailure{Outcome: "busy", Err: errors.New("busy")}}, campaignAudioFixture(t, "0.1"))
	s.startPortCall()
	s.runCampaign(uuid.New(), m.a)
	if got := <-m.done; got != "busy:busy" {
		t.Fatal(got)
	}
	if m.answered {
		t.Fatal("busy call marked answered")
	}
}
func TestCampaignIVRMenuDTMF(t *testing.T) {
	audio := campaignAudioFixture(t, "1")
	leg := newConnectLeg("campaign-customer")
	digits := make(chan string, 1)
	leg.leg.DTMF = digits
	s, m, _ := campaignFixture(t, leg, audio)
	m.a.Payload.Prepared = campaign.Prepared{Flow: &ivr.Flow{EntryNode: "menu", Nodes: []ivr.Node{{ID: "menu", Type: ivr.NodeMenu, Config: map[string]any{"audio_file": audio, "timeout_seconds": 5, "max_retries": 0, "options": map[string]any{"1": "Yes"}}}, {ID: "bye", Type: ivr.NodeHangup}}, Edges: []ivr.Edge{{From: "menu", To: "bye", Condition: "digit:1"}}}}
	var once sync.Once
	leg.leg.Write = func(*rtp.Packet) error { once.Do(func() { digits <- "1" }); return nil }
	s.startPortCall()
	go s.runCampaign(uuid.New(), m.a)
	select {
	case got := <-m.done:
		if got != "completed:" {
			t.Fatal(got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("menu did not consume DTMF")
	}
}
func TestCampaignControlRequiresAuthentication(t *testing.T) {
	s, m, _ := campaignFixture(t, newConnectLeg("customer"), "/missing.ogg")
	r := httptest.NewRequest("POST", "/control/campaign", strings.NewReader(`{"attempt_id":"`+m.a.ID.String()+`"}`))
	w := httptest.NewRecorder()
	s.ControlHandler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("status=%d", w.Code)
	}
	if m.claimed {
		t.Fatal("unauthenticated request claimed a call")
	}
}

func TestCampaignControlReplayDoesNotRedial(t *testing.T) {
	audio := campaignAudioFixture(t, "0.1")
	s, m, o := campaignFixture(t, newConnectLeg("campaign-customer"), audio)
	tenant := uuid.New()
	token := controlToken(t, controlAudience, tenant, time.Minute)
	for range 2 {
		r := httptest.NewRequest("POST", "/control/campaign", strings.NewReader(`{"attempt_id":"`+m.a.ID.String()+`"}`))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ControlHandler().ServeHTTP(w, r)
		if w.Code != 202 {
			t.Fatalf("status=%d %s", w.Code, w.Body.String())
		}
	}
	select {
	case <-m.done:
	case <-time.After(3 * time.Second):
		t.Fatal("call never completed")
	}
	o.mu.Lock()
	n := o.n
	o.mu.Unlock()
	if n != 1 {
		t.Fatalf("replayed control request dialed %d times", n)
	}
}
