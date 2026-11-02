//go:build e2e

package e2e

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

// API is a CRM-style client for Tring's REST API.
type API struct {
	t    *testing.T
	base string
	auth string
}

// runID makes seeded names unique per test binary, so re-runs against the
// same database don't collide (409).
var runID = strconv.FormatInt(time.Now().Unix()%1_000_000, 10)

// uniq returns prefix with this run's suffix.
func uniq(prefix string) string { return prefix + "-" + runID }

// env returns os.Getenv(key), or def when unset.
func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// requireStack skips unless TRING_E2E=1, then logs in as the bootstrap admin
// and returns a client holding a fresh full-scope API key.
func requireStack(t *testing.T) *API {
	t.Helper()
	if os.Getenv("TRING_E2E") != "1" {
		t.Skip("set TRING_E2E=1 with the stack up (e2e/up.sh) to run")
	}
	a := &API{t: t, base: "http://127.0.0.1:" + env("TRING_E2E_API_PORT", "8085")}
	var login struct{ Token string }
	a.do("POST", "/v1/console/login", map[string]string{
		"email":    os.Getenv("TRING_E2E_ADMIN_EMAIL"),
		"password": os.Getenv("TRING_E2E_ADMIN_PASSWORD"),
	}, &login)
	a.auth = login.Token
	var key struct{ Key string }
	a.do("POST", "/v1/api-keys", map[string]string{"name": "e2e-" + t.Name(), "scope": "full"}, &key)
	a.auth = key.Key
	return a
}

// do sends a JSON request (body may be nil) and decodes a 2xx response into
// out (may be nil); anything else fails the test with the status and body.
func (a *API) do(method, path string, body, out any) {
	a.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.base+path, r)
	if err != nil {
		a.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	a.send(req, out)
}

func (a *API) send(req *http.Request, out any) {
	a.t.Helper()
	if a.auth != "" {
		req.Header.Set("Authorization", "Bearer "+a.auth)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode/100 != 2 {
		a.t.Fatalf("%s %s: %d %s", req.Method, req.URL.Path, res.StatusCode, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			a.t.Fatalf("%s %s: decode %s: %v", req.Method, req.URL.Path, b, err)
		}
	}
}

type idResp struct{ ID string }

func (a *API) CreateAgent(sip, name string) string {
	var r idResp
	a.do("POST", "/v1/agents", map[string]string{"sip_username": sip, "display_name": name}, &r)
	return r.ID
}

func (a *API) CreateTeam(name string) {
	a.do("POST", "/v1/teams", map[string]string{"name": name}, nil)
}

func (a *API) AddTeamAgent(team, agentID string) {
	a.do("POST", "/v1/teams/"+team+"/agents", map[string]any{"agent_id": agentID, "position": 1}, nil)
}

func (a *API) CreateTrunk(name, proxy string) string {
	var r idResp
	a.do("POST", "/v1/trunks", map[string]any{"name": name, "proxy_ip": proxy, "register": false}, &r)
	return r.ID
}

// TrunkStatus returns the trunk's status from GET /v1/trunks.
func (a *API) TrunkStatus(id string) string {
	var list []struct{ ID, Status string }
	a.do("GET", "/v1/trunks", nil, &list)
	for _, tr := range list {
		if tr.ID == id {
			return tr.Status
		}
	}
	return ""
}

// WaitTrunkUsable waits until FreeSWITCH has the trunk loaded and up.
func (a *API) WaitTrunkUsable(id string) {
	a.t.Helper()
	a.waitFor(30*time.Second, "trunk "+id+" connecting/online", func() bool {
		s := a.TrunkStatus(id)
		return s == "connecting" || s == "online"
	})
}

func (a *API) CreateNumber(e164, typ, trunkID, flowID string) string {
	body := map[string]any{"e164": e164, "type": typ}
	if trunkID != "" {
		body["trunk_id"] = trunkID
	}
	if flowID != "" {
		body["flow_id"] = flowID
	}
	var r idResp
	a.do("POST", "/v1/numbers", body, &r)
	return r.ID
}

// DeactivateOutboundExcept turns off every active outbound number but keep.
// The gateway dials through the first outbound number's trunk only, so numbers
// left by earlier runs against the same database (trunks now down) must not
// win.
func (a *API) DeactivateOutboundExcept(keep string) {
	var list []struct {
		ID, Type string
		Active   bool
	}
	a.do("GET", "/v1/numbers", nil, &list)
	for _, n := range list {
		if n.Type == "outbound" && n.Active && n.ID != keep {
			a.do("PATCH", "/v1/numbers/"+n.ID, map[string]bool{"active": false}, nil)
		}
	}
}

func (a *API) CreateFlow(name string, definition any) string {
	var r idResp
	a.do("POST", "/v1/flows", map[string]any{"name": name, "is_active": true, "definition": definition}, &r)
	return r.ID
}

// TransferFlow is a flow whose entry transfers the caller, with config the
// transfer node's config ({"agent_sip": …} or {"team": …}).
func TransferFlow(config map[string]string) any {
	return map[string]any{
		"version":    2,
		"entry_node": "start",
		"nodes": []map[string]any{
			{"id": "start", "type": "start", "config": map[string]any{}},
			{"id": "t", "type": "transfer", "config": config},
			{"id": "end", "type": "hangup", "config": map[string]any{}},
		},
		"edges": []map[string]string{
			{"from": "start", "to": "t", "condition": "default"},
			{"from": "t", "to": "end", "condition": "completed"},
		},
	}
}

// UploadAudio uploads a WAV (the api transcodes it to Ogg/Opus) and returns its
// file_path.
func (a *API) UploadAudio(name string, wav []byte) string {
	a.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("name", name)
	fw, err := mw.CreateFormFile("file", name+".wav")
	if err != nil {
		a.t.Fatal(err)
	}
	_, _ = fw.Write(wav)
	_ = mw.Close()
	req, err := http.NewRequest("POST", a.base+"/v1/audio", &buf)
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	var r struct {
		FilePath string `json:"file_path"`
	}
	a.send(req, &r)
	return r.FilePath
}

func (a *API) SetHoldMusic(filePath string) {
	a.do("PATCH", "/v1/tenants/me", map[string]string{"hold_music": filePath}, nil)
}

// AgentToken mints the agent's SDK token, as the CRM's backend does.
func (a *API) AgentToken(agentID string) (token, gatewayURL string) {
	var r struct {
		Token      string `json:"token"`
		GatewayURL string `json:"gateway_url"`
	}
	a.do("GET", "/v1/agents/"+agentID+"/token", nil, &r)
	return r.Token, r.GatewayURL
}

// WaitAgentOnline waits until the agent is ringable (presence `ready` arrives
// before availability is known).
func (a *API) WaitAgentOnline(agentID string) {
	a.t.Helper()
	a.waitFor(10*time.Second, "agent "+agentID+" online", func() bool {
		// status is only filled in on the list response.
		var list []struct{ ID, Status string }
		a.do("GET", "/v1/agents", nil, &list)
		for _, ag := range list {
			if ag.ID == agentID {
				return ag.Status == "online"
			}
		}
		return false
	})
}

// Call places a click-to-call (POST /v1/calls) and returns the call id.
func (a *API) Call(agentSip, to string) string {
	var r idResp
	a.do("POST", "/v1/calls", map[string]string{"agent_id": agentSip, "to": to}, &r)
	return r.ID
}

// Connect bridges two numbers (POST /v1/calls/connect) and returns the call id.
func (a *API) Connect(agentNumber, clientNumber string) string {
	var r idResp
	a.do("POST", "/v1/calls/connect", map[string]string{"agent_number": agentNumber, "client_number": clientNumber}, &r)
	return r.ID
}

// waitFor polls cond every 200 ms until it holds or timeout passes.
func (a *API) waitFor(timeout time.Duration, what string, cond func() bool) {
	a.t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			a.t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// WAV wraps 16-bit mono PCM in a WAV file.
func WAV(pcm []int16, rate int) []byte {
	var b bytes.Buffer
	data := len(pcm) * 2
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + data))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1)) // PCM
	w(uint16(1)) // mono
	w(uint32(rate))
	w(uint32(rate * 2))
	w(uint16(2))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(data))
	w(pcm)
	return b.Bytes()
}
