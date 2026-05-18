// Package webhook delivers tenant-registered HTTP webhooks for Tring
// events. Both the API service (call.ringing) and the orchestrator
// (call.answered, call.ended) use the same Notifier — events flow:
//
//	Caller    Notifier.Emit                Background goroutine
//	------    -------------                --------------------
//	         insert webhook_deliveries
//	         (status='pending')   --->     deliver() with retries
//	         return immediately            update row to 'success' or
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
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/db"
)

// Event names accepted by webhook subscribers. Kept as constants so handler
// code doesn't drift from listener filtering on the integrator side.
const (
	EventCallRinging  = "call.ringing"
	EventCallAnswered = "call.answered"
	EventCallEnded    = "call.ended"
)

type Notifier struct {
	db   *db.DB
	http *http.Client
}

func New(d *db.DB) *Notifier {
	return &Notifier{
		db:   d,
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

// envelope is the JSON shape POSTed to subscribers. `data` is event-
// specific — for call events it carries the full Call row.
type envelope struct {
	Event      string      `json:"event"`
	DeliveryID uuid.UUID   `json:"delivery_id"`
	Created    time.Time   `json:"created"`
	Data       interface{} `json:"data"`
}

// Emit fires the given event to every webhook registered for the tenant
// that subscribes to that type (or all events). It writes pending rows to
// webhook_deliveries synchronously, then dispatches each delivery in a
// background goroutine. Errors during the lookup are logged but never
// surface to the caller — webhooks must not block the originating call
// flow.
func (n *Notifier) Emit(ctx context.Context, tenantID uuid.UUID, eventType string, data interface{}) {
	rows, err := n.db.Pool.Query(ctx, `
		SELECT id, url, secret, events
		FROM webhooks
		WHERE tenant_id = $1
		  AND (cardinality(events) = 0 OR $2 = ANY(events))
	`, tenantID, eventType)
	if err != nil {
		log.Printf("webhook: lookup %s for tenant %s: %v", eventType, tenantID, err)
		return
	}
	defer rows.Close()

	type target struct {
		HookID uuid.UUID
		URL    string
		Secret string
	}
	var targets []target
	for rows.Next() {
		var t target
		var events []string
		if err := rows.Scan(&t.HookID, &t.URL, &t.Secret, &events); err != nil {
			log.Printf("webhook: scan: %v", err)
			continue
		}
		targets = append(targets, t)
	}
	if len(targets) == 0 {
		return
	}

	for _, tgt := range targets {
		var deliveryID uuid.UUID
		payloadBytes, err := json.Marshal(envelope{
			Event:   eventType,
			Created: time.Now().UTC(),
			Data:    data,
		})
		if err != nil {
			log.Printf("webhook: marshal: %v", err)
			continue
		}
		err = n.db.Pool.QueryRow(ctx, `
			INSERT INTO webhook_deliveries (webhook_id, event_type, payload)
			VALUES ($1, $2, $3) RETURNING id
		`, tgt.HookID, eventType, payloadBytes).Scan(&deliveryID)
		if err != nil {
			log.Printf("webhook: insert delivery: %v", err)
			continue
		}
		// Re-marshal with the assigned delivery id so the body the
		// receiver sees matches the header `Tring-Delivery`.
		fullEnv := envelope{
			Event:      eventType,
			DeliveryID: deliveryID,
			Created:    time.Now().UTC(),
			Data:       data,
		}
		body, err := json.Marshal(fullEnv)
		if err != nil {
			log.Printf("webhook: marshal w/ id: %v", err)
			continue
		}
		go n.deliver(deliveryID, tgt.URL, tgt.Secret, eventType, body)
	}
}

// deliver retries up to 5 attempts on non-2xx with backoff 1,2,4,8,16s.
// Cumulative wall time ~30s. After exhaustion, the row goes to 'exhausted'.
func (n *Notifier) deliver(id uuid.UUID, url, secret, eventType string, body []byte) {
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
		status, errStr := n.send(id, url, secret, eventType, body)
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

func (n *Notifier) send(id uuid.UUID, url, secret, eventType string, body []byte) (int, string) {
	ts := time.Now().Unix()
	sig := sign(secret, ts, body)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Sprintf("build request: %v", err)
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
	_, err := n.db.Pool.Exec(context.Background(), `
		UPDATE webhook_deliveries
		SET attempts = $1, response_status = NULLIF($2, 0), last_error = NULLIF($3, '')
		WHERE id = $4
	`, attempts, status, errStr, id)
	if err != nil {
		log.Printf("webhook: record attempt %s: %v", id, err)
	}
}

func (n *Notifier) markDelivered(id uuid.UUID, status int) {
	_, err := n.db.Pool.Exec(context.Background(), `
		UPDATE webhook_deliveries
		SET status='success', delivered_at=now(), response_status=$1, last_error=NULL
		WHERE id=$2
	`, status, id)
	if err != nil {
		log.Printf("webhook: mark delivered %s: %v", id, err)
	}
}

func (n *Notifier) markExhausted(id uuid.UUID, lastStatus int, lastErr string) {
	_, err := n.db.Pool.Exec(context.Background(), `
		UPDATE webhook_deliveries
		SET status='exhausted', response_status=NULLIF($1,0), last_error=NULLIF($2,'')
		WHERE id=$3
	`, lastStatus, lastErr, id)
	if err != nil {
		log.Printf("webhook: mark exhausted %s: %v", id, err)
	}
}
