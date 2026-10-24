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

// hookSpy records every webhook the call log sends.
type hookSpy struct {
	mu   sync.Mutex
	got  []string
	ids  []uuid.UUID
	call []callrow.Call
}

func (h *hookSpy) Send(_, webhookID uuid.UUID, event string, data interface{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.got = append(h.got, event)
	h.ids = append(h.ids, webhookID)
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

// A call whose flow has an "on call end" webhook sends call.ended to it, once,
// with the settled call. Answering sends nothing.
func TestEndedSendsCallEndedToFlowWebhook(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	agentID := seedAgent(t, fx, "alice")
	spy := &hookSpy{}
	l := callcdr.New(fx.DB, nil, t.TempDir(), nil, nil).WithWebhooks(spy)
	hookID := uuid.New()

	roomID := fx.TenantID.String() + "/pstn-hooks"
	l.Started(ctx, fx.TenantID, roomID, "+919800000001", "+919800000002", "phone", "", "caller-pid")
	l.EndWebhook(roomID, hookID)
	if got := l.CallID(roomID); got == uuid.Nil {
		t.Fatal("CallID of a live call is nil")
	}
	l.Answered(roomID, "agent-pid", "alice")
	l.Ended(roomID, "caller_hangup")
	l.Ended(roomID, "again") // idempotent: no second call.ended

	if strings.Join(spy.got, ",") != webhook.EventCallEnded || spy.ids[0] != hookID {
		t.Fatalf("sent %v to %v, want one call.ended to %s", spy.got, spy.ids, hookID)
	}
	ended := spy.call[0]
	if ended.Status != "ended" || ended.EndedAt == nil || ended.DurationSec == nil {
		t.Errorf("call.ended status = %q ended_at = %v duration = %v, want settled", ended.Status, ended.EndedAt, ended.DurationSec)
	}
	if ended.HangupCause == nil || *ended.HangupCause != "caller_hangup" {
		t.Errorf("call.ended hangup_cause = %v, want caller_hangup", ended.HangupCause)
	}
	if ended.AgentID == nil || *ended.AgentID != agentID || ended.TenantID != fx.TenantID {
		t.Errorf("call.ended agent = %v tenant = %s, want %s / %s", ended.AgentID, ended.TenantID, agentID, fx.TenantID)
	}
}

// A flow with no "on call end" webhook sends nothing.
func TestEndedWithoutFlowWebhookSendsNothing(t *testing.T) {
	fx := dbtest.Setup(t)
	spy := &hookSpy{}
	l := callcdr.New(fx.DB, nil, t.TempDir(), nil, nil).WithWebhooks(spy)

	roomID := fx.TenantID.String() + "/web-hooks"
	l.Started(context.Background(), fx.TenantID, roomID, "visitor", "Support", "web", "", "caller-pid")
	l.Ended(roomID, "caller_hangup")

	if len(spy.got) != 0 {
		t.Fatalf("sent %v, want nothing", spy.got)
	}
}

// End to end through the real notifier: the registered endpoint receives
// call.ended, signed, with its custom headers and the call under data.call.
// A disabled webhook receives nothing.
func TestCallEndedDeliveredSignedWithHeaders(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	const secret = "whsec_test"

	type delivery struct {
		event, sig, auth string
		body             []byte
	}
	got := make(chan delivery, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- delivery{r.Header.Get("Tring-Event"), r.Header.Get("Tring-Signature"), r.Header.Get("Authorization"), b}
	}))
	defer srv.Close()
	register := func(active bool) uuid.UUID {
		var id uuid.UUID
		if err := fx.DB.Pool.QueryRow(ctx,
			`INSERT INTO webhooks (tenant_id, name, url, secret, headers, active) VALUES ($1, 'crm', $2, $3, $4, $5) RETURNING id`,
			fx.TenantID, srv.URL, secret, `{"Authorization":"Bearer crm-token"}`, active).Scan(&id); err != nil {
			t.Fatalf("register webhook: %v", err)
		}
		return id
	}
	activeID, disabledID := register(true), register(false)

	l := callcdr.New(fx.DB, nil, t.TempDir(), nil, nil).WithWebhooks(webhook.New(fx.DB).WithHTTPClient(srv.Client())) // srv is loopback: bypass the SSRF guard
	for i, id := range []uuid.UUID{disabledID, activeID} {
		roomID := fx.TenantID.String() + "/pstn-e2e-" + string(rune('a'+i))
		l.Started(ctx, fx.TenantID, roomID, "+919800000003", "+919800000004", "phone", "", "caller-pid")
		l.EndWebhook(roomID, id)
		l.Ended(roomID, "agent")
	}

	select {
	case d := <-got:
		// Tring-Signature: t=<unix>,v1=<hex(hmac_sha256(secret, "<unix>.<body>"))>
		parts := map[string]string{}
		for _, kv := range strings.Split(d.sig, ",") {
			k, v, _ := strings.Cut(kv, "=")
			parts[k] = v
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(parts["t"] + "." + string(d.body)))
		if !hmac.Equal([]byte(parts["v1"]), []byte(hex.EncodeToString(mac.Sum(nil)))) {
			t.Errorf("bad signature %q", d.sig)
		}
		if d.auth != "Bearer crm-token" {
			t.Errorf("Authorization = %q, want the webhook's custom header", d.auth)
		}
		var env struct {
			Event string `json:"event"`
			Data  struct {
				Call callrow.Call `json:"call"`
			} `json:"data"`
		}
		if err := json.Unmarshal(d.body, &env); err != nil || env.Event != webhook.EventCallEnded || d.event != webhook.EventCallEnded || env.Data.Call.TenantID != fx.TenantID {
			t.Errorf("body = %s (err %v)", d.body, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call.ended was not delivered")
	}
	select {
	case d := <-got:
		t.Fatalf("a second delivery arrived (the disabled webhook?): %s", d.body)
	case <-time.After(300 * time.Millisecond):
	}
}
