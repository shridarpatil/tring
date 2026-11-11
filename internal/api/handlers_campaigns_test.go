package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/tring/internal/campaign"
	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/tts"
)

func apiCampaignConfig() campaign.Config {
	return campaign.Config{Type: "tts", Text: "Appointment tomorrow", CallerID: "+912265915000", Timezone: "Asia/Kolkata", WindowStart: "09:00", WindowEnd: "18:00", Concurrency: 2, CallsPerMinute: 30, MaxAttempts: 1, RetryMinutes: 30, RingSeconds: 10, MaxSeconds: 30}
}
func campaignRequest(t *testing.T, s *Server, f *dbtest.Fixture, method, path string, body any, want int) *httptest.ResponseRecorder {
	t.Helper()
	b := ""
	if body != nil {
		b = string(campaign.JSON(body))
	}
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, authedRequest(t, s, f, method, path, b))
	if w.Code != want {
		t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}
func campaignFromResponse(t *testing.T, w *httptest.ResponseRecorder) campaign.Campaign {
	t.Helper()
	var c campaign.Campaign
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCampaignAPIImportLifecycleAndAuthorization(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	c := campaignFromResponse(t, campaignRequest(t, s, f, "POST", "/v1/campaigns", map[string]any{"name": "Reminders", "config": apiCampaignConfig()}, 201))
	path := "/v1/campaigns/" + c.ID.String()
	campaignRequest(t, s, f, "POST", path+"/prepare", map[string]any{"revision": c.Revision}, 400) // Disabled TTS is visible, never silent.
	res := campaignRequest(t, s, f, "POST", path+"/recipients", map[string]any{"recipients": []map[string]string{{"phone": "9876543210"}, {"phone": "+919876543210"}, {"phone": "invalid"}}}, 200)
	var result struct {
		Added, Duplicates int
		Rejected          []any
	}
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Added != 1 || result.Duplicates != 1 || len(result.Rejected) != 1 {
		t.Fatal(res.Body.String())
	}
	c = campaignFromResponse(t, campaignRequest(t, s, f, "GET", path, nil, 200))
	if c.Counts["pending"] != 1 {
		t.Fatal(c.Counts)
	}
	// Non-admin console sessions cannot even list campaigns.
	agent := f.AddUser(t, "agent", "agent@campaign.test")
	token, _, err := s.IssueConsoleToken(agent, f.TenantID, "agent@campaign.test", "agent", 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/v1/campaigns", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("agent campaign access=%d", w.Code)
	}
	campaignRequest(t, s, f, "GET", "/v1/campaigns/"+uuid.NewString(), nil, 404)
	campaignRequest(t, s, f, "POST", path+"/cancel", map[string]any{"revision": c.Revision}, 200)
	campaignRequest(t, s, f, "PUT", path, map[string]any{"name": "Changed", "config": apiCampaignConfig(), "revision": c.Revision}, 409)
}
func TestCampaignCSVValidationAndExport(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	id, err := s.campaignStore().Create(context.Background(), f.TenantID, "CSV", apiCampaignConfig())
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/campaigns/" + id.String()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "recipients.csv")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("phone,external_ref\n9876543210,=1+1\n9876543211,customer-2\n"))
	_ = form.Close()
	r := authedRequest(t, s, f, "POST", path+"/recipients", "")
	r.Body = ioBody(&body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = campaignRequest(t, s, f, "GET", path+"/export", nil, 200)
	if !strings.Contains(w.Body.String(), "'=1+1") || !strings.Contains(w.Body.String(), "'+919876543210") {
		t.Fatal("CSV formula escaping missing:", w.Body.String())
	}
}

type readCloser struct{ *bytes.Buffer }

func (readCloser) Close() error         { return nil }
func ioBody(b *bytes.Buffer) readCloser { return readCloser{b} }
func TestCampaignAudioPreparationOwnsPlayableSnapshot(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	s.audioDir = t.TempDir()
	dir := filepath.Join(s.audioDir, f.TenantID.String())
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg required")
	}
	// Ogg can contain Vorbis; preparing must re-encode, not trust the extension.
	source := filepath.Join(dir, "uploaded.ogg")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440", "-t", "0.2", "-c:a", "libvorbis", source).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	var audioID uuid.UUID
	if err := f.DB.Pool.QueryRow(context.Background(), "INSERT INTO audio_assets (tenant_id,name,file_path,format,size_bytes) VALUES ($1,'Source',$2,'ogg',1) RETURNING id", f.TenantID, source).Scan(&audioID); err != nil {
		t.Fatal(err)
	}
	cfg := apiCampaignConfig()
	cfg.Type = "audio"
	cfg.AudioID = audioID.String()
	p, err := s.prepareCampaign(context.Background(), f.TenantID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.AudioFile == source {
		t.Fatal("snapshot references mutable library file")
	}
	if err = os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err = checkCampaignAudio(p.AudioFile, cfg.MaxSeconds); err != nil {
		t.Fatal("deleting original broke snapshot", err)
	}
	other := uuid.New()
	outside := filepath.Join(s.audioDir, other.String())
	if err = os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "sound.ogg")
	if err = os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.campaignAudio(context.Background(), f.TenantID, secret, 30); err == nil {
		t.Fatal("copied another tenant's audio")
	}
	symlink := filepath.Join(dir, "escape.ogg")
	if err = os.Symlink(secret, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err = s.campaignAudio(context.Background(), f.TenantID, symlink, 30); err == nil {
		t.Fatal("followed cross-tenant symlink")
	}
}
func TestCampaignTTSPrepareAndSchedule(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	s.audioDir = t.TempDir()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg required")
	}
	// The fake Piper isolates synthesis plumbing from installing a voice model.
	piper := filepath.Join(s.audioDir, "piper")
	script := `#!/bin/sh
out=""
while [ $# -gt 0 ]; do case "$1" in -f) out="$2"; shift;; esac; shift; done
cat >/dev/null
exec ffmpeg -v error -f lavfi -i anullsrc=r=22050:cl=mono -t 0.2 -y "$out"
`
	if err := os.WriteFile(piper, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(s.audioDir, "voice.onnx")
	if err := os.WriteFile(model, []byte("stub"), 0600); err != nil {
		t.Fatal(err)
	}
	s.tts = tts.New(tts.Config{PiperBin: piper, Model: model, CacheDir: filepath.Join(s.audioDir, "tts")})
	var trunk uuid.UUID
	if err := f.DB.Pool.QueryRow(context.Background(), "INSERT INTO trunks(tenant_id,name) VALUES($1,'campaign-test') RETURNING id", f.TenantID).Scan(&trunk); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(context.Background(), "INSERT INTO numbers(tenant_id,e164,type,provider,trunk_id) VALUES($1,'+912265915000','outbound','tata',$2)", f.TenantID, trunk); err != nil {
		t.Fatal(err)
	}
	c := campaignFromResponse(t, campaignRequest(t, s, f, "POST", "/v1/campaigns", map[string]any{"name": "TTS", "config": apiCampaignConfig()}, 201))
	path := "/v1/campaigns/" + c.ID.String()
	c = campaignFromResponse(t, campaignRequest(t, s, f, "POST", path+"/prepare", map[string]any{"revision": c.Revision}, 200))
	if c.Prepared == nil || c.Prepared.AudioFile == "" {
		t.Fatal("no generated audio")
	}
	campaignRequest(t, s, f, "POST", path+"/recipients", map[string]any{"recipients": []map[string]string{{"phone": "9876543210"}}}, 200)
	c = campaignFromResponse(t, campaignRequest(t, s, f, "GET", path, nil, 200))
	campaignRequest(t, s, f, "POST", path+"/schedule", map[string]any{"revision": c.Revision, "scheduled_at": time.Now().Add(-time.Minute)}, 400)
	at := time.Now().Add(time.Hour)
	c = campaignFromResponse(t, campaignRequest(t, s, f, "POST", path+"/schedule", map[string]any{"revision": c.Revision, "scheduled_at": at}, 200))
	if c.Status != "scheduled" {
		t.Fatal(c.Status)
	}
	campaignRequest(t, s, f, "PUT", path, map[string]any{"revision": c.Revision, "name": "changed", "config": apiCampaignConfig()}, 409)
}
func TestCampaignIVRValidation(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	var flowID uuid.UUID
	raw := `{"entry_node":"start","nodes":[{"id":"start","type":"start"},{"id":"menu","type":"menu","config":{"options":{"1":"yes"}}},{"id":"end","type":"hangup"}],"edges":[{"from":"start","to":"menu","condition":"default"},{"from":"menu","to":"end","condition":"digit:1"}]}`
	if err := f.DB.Pool.QueryRow(context.Background(), "INSERT INTO flows(tenant_id,name,definition) VALUES($1,'campaign',$2) RETURNING id", f.TenantID, raw).Scan(&flowID); err != nil {
		t.Fatal(err)
	}
	cfg := apiCampaignConfig()
	cfg.Type = "ivr"
	cfg.FlowID = flowID.String()
	p, err := s.prepareCampaign(context.Background(), f.TenantID, cfg)
	if err != nil || p.Flow == nil {
		t.Fatalf("IVR: %v", err)
	}
	if _, err = s.prepareCampaign(context.Background(), uuid.New(), cfg); err == nil {
		t.Fatal("cross-tenant flow prepared")
	}
	if _, err = f.DB.Pool.Exec(context.Background(), "UPDATE flows SET definition=$2 WHERE id=$1", flowID, fmt.Sprintf(`{"entry_node":"goto","nodes":[{"id":"goto","type":"goto_flow","config":{"flow_id":"%s"}}]}`, uuid.New())); err != nil {
		t.Fatal(err)
	}
	if _, err = s.prepareCampaign(context.Background(), f.TenantID, cfg); err == nil {
		t.Fatal("unsupported goto_flow accepted")
	}
}

func TestAgentCampaignPreparationRequiresTenantTeam(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	cfg := apiCampaignConfig()
	cfg.Type, cfg.Text, cfg.Team, cfg.AgentWaitSeconds = "agent", "", "sales", 120
	c := campaignFromResponse(t, campaignRequest(t, s, f, "POST", "/v1/campaigns", map[string]any{"name": "Team calls", "config": cfg}, 201))
	path := "/v1/campaigns/" + c.ID.String()
	// The API may store a draft before a team exists, but may not prepare it.
	campaignRequest(t, s, f, "POST", path+"/prepare", map[string]any{"revision": c.Revision}, 400)
	other := uuid.New()
	if _, err := f.DB.Pool.Exec(context.Background(), `INSERT INTO tenants(id,name,sip_domain,api_key_hash,api_key_prefix) VALUES($1,'Other','agent-campaign-other.test','agent-campaign-test-hash','deprecated--')`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(context.Background(), `INSERT INTO teams(tenant_id,name) VALUES($1,'sales')`, other); err != nil {
		t.Fatal(err)
	}
	campaignRequest(t, s, f, "POST", path+"/prepare", map[string]any{"revision": c.Revision}, 400)
	campaignRequest(t, s, f, "POST", "/v1/teams", map[string]any{"name": "sales"}, 201)
	c = campaignFromResponse(t, campaignRequest(t, s, f, "POST", path+"/prepare", map[string]any{"revision": c.Revision}, 200))
	if c.Prepared == nil || c.Prepared.Team != "sales" || c.Prepared.AudioFile != "" {
		t.Fatalf("prepared=%+v", c.Prepared)
	}
	if err := s.checkCampaignPrepared(c.Prepared, c.Config.MaxSeconds); err != nil {
		t.Fatal(err)
	}
	// A removed team is rejected again at launch, before any attempt is queued.
	if _, err := f.DB.Pool.Exec(context.Background(), `DELETE FROM teams WHERE tenant_id=$1 AND name='sales'`, f.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := s.campaignCaller(context.Background(), f.TenantID, c.Config); err == nil || !strings.Contains(err.Error(), "team") {
		t.Fatalf("launch validation=%v", err)
	}
}
