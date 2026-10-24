// Package webhook delivers tenant-registered HTTP webhooks. A webhook is a
// registered URL (plus optional headers) with an active/disabled status; IVR
// flows pick one by id — a node's "notify webhook" (ivr.node) and a flow's
// "on call end" webhook (call.ended). The gateway sends both:
//
//	Caller    Notifier.Send                Background goroutine
//	------    -------------                --------------------
//	         look up the webhook
//	         (Redis-cached per tenant) --> skip if disabled / gone
//	                                       insert webhook_deliveries
//	                                       deliver() with retries
//	                                       update row to 'success' or
//	                                       'exhausted', record last_error
//
// Signing follows Stripe's pattern. The `Tring-Signature` header is:
//
//	Tring-Signature: t=<unix>,v1=<hex(hmac_sha256(secret, "<unix>.<body>"))>
//
// Receivers verify by recomputing v1 over `<t>.<raw_body>` in constant
// time. `t` is included to enable replay-window enforcement.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/db"
	"github.com/shridarpatil/tring/internal/queries"
	"github.com/shridarpatil/tring/internal/safehttp"
	"github.com/shridarpatil/tring/internal/secretbox"
)

// Event names sent to webhooks. Kept as constants so handler code doesn't
// drift from listener filtering on the integrator side.
const (
	EventIVRNode   = "ivr.node"   // an IVR node with a webhook finished (a transfer: answered or not)
	EventCallEnded = "call.ended" // a call whose flow has an "on call end" webhook ended
)

type Notifier struct {
	db      *db.DB
	q       *queries.Queries
	http    *http.Client
	log     *slog.Logger
	cache   *cache.Cache   // nil = no caching (every send reads the DB)
	secrets *secretbox.Box // nil = headers stored as plaintext
}

// DeliveryTimeout bounds one webhook delivery attempt.
const DeliveryTimeout = 10 * time.Second

// cacheTTL backstops a missed invalidation; the api deletes CacheKey on every
// webhook create/update/delete, so edits apply on the next send.
const cacheTTL = time.Hour

// CacheKey is the Redis key holding a tenant's webhooks. Exported so the api
// can invalidate it.
func CacheKey(tenantID uuid.UUID) string { return "webhooks:" + tenantID.String() }

// New builds a Notifier. Webhook URLs are tenant-controlled, so deliveries go
// through an SSRF-guarded client (internal/safehttp) with no private-range
// allowlist; WithHTTPClient swaps in one built with the operator's allowlist.
func New(d *db.DB) *Notifier {
	return &Notifier{
		db:   d,
		q:    queries.MustLoad(),
		http: safehttp.NewClient(nil, DeliveryTimeout),
		log:  slog.Default().With("component", "webhook"),
	}
}

// WithLogger sets the notifier's logger (scoped component=webhook) and returns
// it for chaining.
func (n *Notifier) WithLogger(l *slog.Logger) *Notifier {
	if l != nil {
		n.log = l.With("component", "webhook")
	}
	return n
}

// WithHTTPClient sets the client deliveries are POSTed with and returns the
// notifier for chaining. It must be a safehttp client (or a test client);
// nil keeps the default.
func (n *Notifier) WithHTTPClient(c *http.Client) *Notifier {
	if c != nil {
		n.http = c
	}
	return n
}

// WithCache caches each tenant's webhooks in Redis (nil = off) and returns the
// notifier for chaining.
func (n *Notifier) WithCache(c *cache.Cache) *Notifier {
	n.cache = c
	return n
}

// WithSecrets sets the key that opens sealed webhook headers and returns the
// notifier for chaining.
func (n *Notifier) WithSecrets(b *secretbox.Box) *Notifier {
	n.secrets = b
	return n
}

// envelope is the JSON shape POSTed to a webhook. `data` is event-specific.
type envelope struct {
	Event      string      `json:"event"`
	DeliveryID uuid.UUID   `json:"delivery_id"`
	Created    time.Time   `json:"created"`
	Data       interface{} `json:"data"`
}

// hook is one webhook as the notifier needs it (also the cached form).
// Headers stays sealed in the cache and is opened per send.
type hook struct {
	URL     string `json:"url"`
	Secret  string `json:"secret"`
	Headers string `json:"headers"`
	Active  bool   `json:"active"`
}

// Send delivers event to the tenant's webhook id, in the background: the call
// path never waits on the lookup, the delivery row or the HTTP request. A
// disabled, deleted or unknown webhook is skipped.
func (n *Notifier) Send(tenantID, webhookID uuid.UUID, event string, data interface{}) {
	go n.send(tenantID, webhookID, event, data)
}

func (n *Notifier) send(tenantID, webhookID uuid.UUID, event string, data interface{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hooks, err := n.tenantHooks(ctx, tenantID)
	if err != nil {
		n.log.Error("webhook lookup failed", "tenant", tenantID, "webhook", webhookID, "err", err)
		return
	}
	h, ok := hooks[webhookID.String()]
	if !ok || !h.Active {
		return // deleted (a flow still names it) or disabled
	}
	headers, err := n.openHeaders(h.Headers)
	if err != nil {
		n.log.Error("webhook headers unreadable; not sent", "webhook", webhookID, "err", err)
		return
	}

	var deliveryID uuid.UUID
	if err := n.db.Pool.QueryRow(ctx, n.q.InsertWebhookDelivery, webhookID, event, []byte("{}")).Scan(&deliveryID); err != nil {
		n.log.Error("insert delivery failed", "webhook", webhookID, "err", err)
		return
	}
	// The body carries the delivery id, so it's built once the row exists;
	// the stored payload is then updated to the exact body sent.
	body, err := json.Marshal(envelope{Event: event, DeliveryID: deliveryID, Created: time.Now().UTC(), Data: data})
	if err != nil {
		n.log.Error("marshal envelope failed", "err", err)
		return
	}
	if _, err := n.db.Pool.Exec(ctx, n.q.SetDeliveryPayload, body, deliveryID); err != nil {
		n.log.Warn("store delivery payload failed", "delivery", deliveryID, "err", err)
	}
	n.deliver(deliveryID, h.URL, h.Secret, headers, event, body)
}

// tenantHooks returns a tenant's webhooks by id, from the cache when warm.
func (n *Notifier) tenantHooks(ctx context.Context, tenantID uuid.UUID) (map[string]hook, error) {
	key := CacheKey(tenantID)
	if b, ok := n.cache.Get(ctx, key); ok {
		var hooks map[string]hook
		if json.Unmarshal(b, &hooks) == nil {
			return hooks, nil
		}
	}
	rows, err := n.db.Pool.Query(ctx, n.q.ListTenantWebhooks, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hooks := map[string]hook{}
	for rows.Next() {
		var id uuid.UUID
		var h hook
		if err := rows.Scan(&id, &h.URL, &h.Secret, &h.Headers, &h.Active); err != nil {
			return nil, err
		}
		hooks[id.String()] = h
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if b, err := json.Marshal(hooks); err == nil {
		n.cache.Set(ctx, key, b, cacheTTL)
	}
	return hooks, nil
}

// openHeaders decodes a stored headers value ('' = none).
func (n *Notifier) openHeaders(stored string) (map[string]string, error) {
	if stored == "" {
		return nil, nil
	}
	plain := stored
	if secretbox.IsSealed(stored) {
		if n.secrets == nil {
			return nil, fmt.Errorf("headers are encrypted but TRING_SECRETS_KEY is not set")
		}
		var err error
		if plain, err = n.secrets.Open(stored); err != nil {
			return nil, err
		}
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(plain), &headers); err != nil {
		return nil, err
	}
	return headers, nil
}

// deliver retries up to 5 attempts on non-2xx with backoff 1,2,4,8,16s.
// Cumulative wall time ~30s. After exhaustion, the row goes to 'exhausted'.
func (n *Notifier) deliver(id uuid.UUID, url, secret string, headers map[string]string, eventType string, body []byte) {
	backoff := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
	}
	var lastErr string
	var lastStatus int
	for attempt := 0; attempt <= len(backoff); attempt++ {
		if attempt > 0 {
			time.Sleep(backoff[attempt-1])
		}
		status, errStr := n.post(id, url, secret, headers, eventType, body)
		n.recordAttempt(id, attempt+1, status, errStr)
		if status >= 200 && status < 300 {
			n.markDelivered(id, status)
			return
		}
		lastStatus = status
		lastErr = errStr
	}
	n.markExhausted(id, lastStatus, lastErr)
}

func (n *Notifier) post(id uuid.UUID, url, secret string, headers map[string]string, eventType string, body []byte) (int, string) {
	ts := time.Now().Unix()
	sig := sign(secret, ts, body)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Sprintf("build request: %v", err)
	}
	for k, v := range headers { // the integrator's own headers (auth, routing); ours below win
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Tring-Webhook/0.1")
	req.Header.Set("Tring-Event", eventType)
	req.Header.Set("Tring-Delivery", id.String())
	req.Header.Set("Tring-Signature", fmt.Sprintf("t=%d,v1=%s", ts, sig))

	resp, err := n.http.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, ""
	}
	return resp.StatusCode, fmt.Sprintf("non-2xx: %d", resp.StatusCode)
}

func sign(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func (n *Notifier) recordAttempt(id uuid.UUID, attempts, status int, errStr string) {
	_, err := n.db.Pool.Exec(context.Background(), n.q.UpdateDeliveryAttempt,
		attempts, status, errStr, id)
	if err != nil {
		n.log.Error("record attempt failed", "delivery", id, "err", err)
	}
}

func (n *Notifier) markDelivered(id uuid.UUID, status int) {
	_, err := n.db.Pool.Exec(context.Background(), n.q.MarkDeliverySuccess, status, id)
	if err != nil {
		n.log.Error("mark delivered failed", "delivery", id, "err", err)
	}
}

func (n *Notifier) markExhausted(id uuid.UUID, lastStatus int, lastErr string) {
	_, err := n.db.Pool.Exec(context.Background(), n.q.MarkDeliveryExhausted,
		lastStatus, lastErr, id)
	if err != nil {
		n.log.Error("mark exhausted failed", "delivery", id, "err", err)
	}
}
