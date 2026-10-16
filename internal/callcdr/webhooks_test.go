package callcdr_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/callcdr"
	"github.com/shridarpatil/tring/internal/callrow"
	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/webhook"
)

// hookSpy records every webhook the call log emits.
type hookSpy struct {
	mu   sync.Mutex
	got  []string
	call []callrow.Call
}

func (h *hookSpy) Emit(_ context.Context, _ uuid.UUID, event string, data interface{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.got = append(h.got, event)
	if m, ok := data.(map[string]interface{}); ok {
		if c, ok := m["call"].(callrow.Call); ok {
			h.call = append(h.call, c)
		}
	}
}

func seedAgent(t *testing.T, fx *dbtest.Fixture, sip string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := fx.DB.Pool.QueryRow(context.Background(),
		`INSERT INTO agents (tenant_id, sip_username, sip_password) VALUES ($1, $2, 'x') RETURNING id`,
		fx.TenantID, sip).Scan(&id); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return id
}

func TestAnsweredAndEndedEmitCallWebhooks(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	agentID := seedAgent(t, fx, "alice")
	spy := &hookSpy{}
	l := callcdr.New(fx.DB, nil, t.TempDir(), nil, nil).WithWebhooks(spy)

	roomID := fx.TenantID.String() + "/pstn-hooks"
	l.Started(ctx, fx.TenantID, roomID, "+919800000001", "+919800000002", "phone", "", "caller-pid")
	l.Answered(roomID, "caller-pid", "alice") // the caller's own join: not an answer
	l.Answered(roomID, "agent-pid", "alice")
	l.Ended(roomID, "caller_hangup")
	l.Ended(roomID, "again") // idempotent: no second call.ended

	if want := []string{webhook.EventCallAnswered, webhook.EventCallEnded}; strings.Join(spy.got, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", spy.got, want)
	}
	answered, ended := spy.call[0], spy.call[1]
	if answered.Status != "answered" || answered.AnsweredAt == nil {
		t.Errorf("call.answered status = %q answered_at = %v, want answered + set", answered.Status, answered.AnsweredAt)
	}
	if answered.AgentID == nil || *answered.AgentID != agentID || answered.AgentName == nil || *answered.AgentName != "alice" {
		t.Errorf("call.answered agent = %v / %v, want %s / alice", answered.AgentID, answered.AgentName, agentID)
	}
	if ended.Status != "ended" || ended.EndedAt == nil || ended.DurationSec == nil {
		t.Errorf("call.ended status = %q ended_at = %v duration = %v, want settled", ended.Status, ended.EndedAt, ended.DurationSec)
	}
	if ended.HangupCause == nil || *ended.HangupCause != "caller_hangup" {
		t.Errorf("call.ended hangup_cause = %v, want caller_hangup", ended.HangupCause)
	}
	if answered.ID != ended.ID || answered.TenantID != fx.TenantID || answered.Source != "phone" {
		t.Errorf("payloads don't describe the same tenant call: %+v / %+v", answered, ended)
	}
}

// A call nobody answers still ends: only call.ended fires.
func TestUnansweredCallEmitsOnlyEnded(t *testing.T) {
	fx := dbtest.Setup(t)
	spy := &hookSpy{}
	l := callcdr.New(fx.DB, nil, t.TempDir(), nil, nil).WithWebhooks(spy)

	roomID := fx.TenantID.String() + "/web-hooks"
	l.Started(context.Background(), fx.TenantID, roomID, "visitor", "Support", "web", "", "caller-pid")
	l.Ended(roomID, "caller_hangup")

	if strings.Join(spy.got, ",") != webhook.EventCallEnded {
		t.Fatalf("events = %v, want only call.ended", spy.got)
	}
}

// End to end through the real notifier: a subscribed endpoint receives both
// events, signed, with the call object under data.call.
func TestCallWebhooksDeliveredAndSigned(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	seedAgent(t, fx, "bob")
	const secret = "whsec_test"

	type delivery struct {
		event, sig string
		body       []byte
	}
	got := make(chan delivery, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- delivery{r.Header.Get("Tring-Event"), r.Header.Get("Tring-Signature"), b}
	}))
	defer srv.Close()
	if _, err := fx.DB.Pool.Exec(ctx, `INSERT INTO webhooks (tenant_id, url, secret, events) VALUES ($1, $2, $3, $4)`,
		fx.TenantID, srv.URL, secret, []string{webhook.EventCallAnswered, webhook.EventCallEnded}); err != nil {
		t.Fatalf("register webhook: %v", err)
	}

	l := callcdr.New(fx.DB, nil, t.TempDir(), nil, nil).WithWebhooks(webhook.New(fx.DB))
	roomID := fx.TenantID.String() + "/pstn-e2e"
	l.Started(ctx, fx.TenantID, roomID, "+919800000003", "+919800000004", "phone", "", "caller-pid")
	l.Answered(roomID, "agent-pid", "bob")
	l.Ended(roomID, "agent_hangup")

	seen := map[string]bool{}
	for range 2 {
		select {
		case d := <-got:
			seen[d.event] = true
			// Tring-Signature: t=<unix>,v1=<hex(hmac_sha256(secret, "<unix>.<body>"))>
			parts := map[string]string{}
			for _, kv := range strings.Split(d.sig, ",") {
				k, v, _ := strings.Cut(kv, "=")
				parts[k] = v
			}
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(parts["t"] + "." + string(d.body)))
			if !hmac.Equal([]byte(parts["v1"]), []byte(hex.EncodeToString(mac.Sum(nil)))) {
				t.Errorf("%s: bad signature %q", d.event, d.sig)
			}
			var env struct {
				Event string `json:"event"`
				Data  struct {
					Call callrow.Call `json:"call"`
				} `json:"data"`
			}
			if err := json.Unmarshal(d.body, &env); err != nil || env.Event != d.event || env.Data.Call.TenantID != fx.TenantID {
				t.Errorf("%s: body = %s (err %v)", d.event, d.body, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out; got %v", seen)
		}
	}
	if !seen[webhook.EventCallAnswered] || !seen[webhook.EventCallEnded] {
		t.Fatalf("delivered = %v, want call.answered and call.ended", seen)
	}
}
