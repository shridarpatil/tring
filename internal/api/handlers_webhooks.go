package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shridarpatil/tring/internal/safehttp"
)

type Webhook struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	URL       string    `json:"url"`
	Secret    string    `json:"secret,omitempty"` // returned only on create
	Events    []string  `json:"events"`
	CreatedAt time.Time `json:"created_at"`
}

type createWebhookRequest struct {
	URL    string   `json:"url"`
	Events []string `json:"events"` // empty => all events
	Secret string   `json:"secret"` // optional; auto-generated if empty
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req createWebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	// Up-front SSRF check (literal private IPs, localhost, non-http schemes);
	// the delivery client re-checks the resolved address on every attempt.
	if err := safehttp.ValidateURL(req.URL, s.outboundAllow); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Events == nil {
		req.Events = []string{}
	}
	if req.Secret == "" {
		req.Secret = generateWebhookSecret()
	}

	var h Webhook
	err := s.db.Pool.QueryRow(r.Context(), s.q.CreateWebhook,
		t.ID, req.URL, req.Secret, req.Events).
		Scan(&h.ID, &h.TenantID, &h.URL, &h.Secret, &h.Events, &h.CreatedAt)
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "insert webhook failed", err)
		return
	}
	writeJSON(w, http.StatusCreated, h)
}

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	rows, err := s.db.Pool.Query(r.Context(), s.q.ListWebhooks, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	out := []Webhook{}
	for rows.Next() {
		var h Webhook
		if err := rows.Scan(&h.ID, &h.TenantID, &h.URL, &h.Events, &h.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, h)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	tag, err := s.db.Pool.Exec(r.Context(), s.q.DeleteWebhook, id, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "webhook not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Inspecting deliveries is useful enough during integration debugging that
// it's worth one read endpoint. Phase 2 will add replay/retry actions.
type WebhookDelivery struct {
	ID             uuid.UUID  `json:"id"`
	WebhookID      uuid.UUID  `json:"webhook_id"`
	EventType      string     `json:"event_type"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	LastError      *string    `json:"last_error,omitempty"`
	ResponseStatus *int       `json:"response_status,omitempty"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

func (s *Server) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	// Verify ownership.
	var owner uuid.UUID
	err = s.db.Pool.QueryRow(r.Context(), s.q.GetWebhookOwner, id).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) || owner != t.ID {
		writeError(w, http.StatusNotFound, "webhook not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), s.q.ListWebhookDeliveries, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	out := []WebhookDelivery{}
	for rows.Next() {
		var d WebhookDelivery
		if err := rows.Scan(&d.ID, &d.WebhookID, &d.EventType, &d.Status,
			&d.Attempts, &d.LastError, &d.ResponseStatus, &d.DeliveredAt, &d.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, out)
}

func generateWebhookSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "whsec_" + hex.EncodeToString(b)
}
