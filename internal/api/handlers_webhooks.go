package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shridarpatil/tring/internal/safehttp"
	"github.com/shridarpatil/tring/internal/webhook"
)

// Webhook is a registered URL that IVR flows pick by id: a node's "notify
// webhook" or a flow's "on call end" webhook. Header values are write-only:
// only their names come back.
type Webhook struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	Secret      string    `json:"secret,omitempty"` // returned only on create
	HeaderNames []string  `json:"header_names"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type createWebhookRequest struct {
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Active  *bool             `json:"active"` // default true
	Secret  string            `json:"secret"` // optional; auto-generated if empty
}

// updateWebhookRequest: an omitted field is unchanged. headers replaces the
// whole set ({} clears it).
type updateWebhookRequest struct {
	Name    *string            `json:"name"`
	URL     *string            `json:"url"`
	Headers *map[string]string `json:"headers"`
	Active  *bool              `json:"active"`
}

const (
	maxWebhookHeaders     = 20
	maxWebhookHeaderValue = 2048
)

// reservedWebhookHeaders are set by the sender (or the HTTP client) and can't
// be overridden.
var reservedWebhookHeaders = map[string]bool{
	"Content-Type": true, "Content-Length": true, "Host": true, "User-Agent": true,
	"Connection": true, "Transfer-Encoding": true,
}

// validateWebhookHeaders checks names are HTTP tokens that don't shadow ours
// and values carry no control characters (no header injection).
func validateWebhookHeaders(h map[string]string) error {
	if len(h) > maxWebhookHeaders {
		return fmt.Errorf("at most %d headers", maxWebhookHeaders)
	}
	for k, v := range h {
		if k == "" || strings.IndexFunc(k, func(r rune) bool { return !isTokenRune(r) }) >= 0 {
			return fmt.Errorf("invalid header name %q", k)
		}
		canon := http.CanonicalHeaderKey(k)
		if reservedWebhookHeaders[canon] || strings.HasPrefix(canon, "Tring-") {
			return fmt.Errorf("header %q is set by Tring", k)
		}
		if len(v) > maxWebhookHeaderValue || strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 && r != '\t' || r == 0x7f }) {
			return fmt.Errorf("invalid value for header %q", k)
		}
	}
	return nil
}

// isTokenRune reports whether r may appear in an HTTP header name (RFC 9110 token).
func isTokenRune(r rune) bool {
	return r < 0x7f && (r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
		strings.ContainsRune("!#$%&'*+-.^_`|~", r))
}

// sealWebhookHeaders encodes headers for storage ('' = none).
func (s *Server) sealWebhookHeaders(h map[string]string) string {
	if len(h) == 0 {
		return ""
	}
	b, _ := json.Marshal(h)
	return s.sealSecret(string(b))
}

// webhookHeaderNames lists the stored headers' names, sorted, for display.
// Unreadable headers (wrong key) list none rather than failing the request.
func (s *Server) webhookHeaderNames(stored string) []string {
	names := []string{}
	if stored == "" {
		return names
	}
	plain, err := s.openSecret(stored)
	if err != nil {
		return names
	}
	var h map[string]string
	if json.Unmarshal([]byte(plain), &h) != nil {
		return names
	}
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// invalidateWebhooks drops the gateway's cached copy of a tenant's webhooks so
// a change applies on the next send. Best-effort: a miss means staleness until
// the cache TTL.
func (s *Server) invalidateWebhooks(ctx context.Context, tenantID uuid.UUID) {
	if err := s.cache.Delete(ctx, webhook.CacheKey(tenantID)); err != nil {
		s.log.Warn("webhook cache invalidate failed", "tenant", tenantID, "err", err)
	}
}

// scanWebhook reads a webhook row (without the secret).
func (s *Server) scanWebhook(row pgx.Row) (Webhook, error) {
	var h Webhook
	var headers string
	err := row.Scan(&h.ID, &h.TenantID, &h.Name, &h.URL, &headers, &h.Active, &h.CreatedAt, &h.UpdatedAt)
	h.HeaderNames = s.webhookHeaderNames(headers)
	return h, err
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req createWebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	if req.Name == "" {
		req.Name = req.URL
	}
	// Up-front SSRF check (literal private IPs, localhost, non-http schemes);
	// the delivery client re-checks the resolved address on every attempt.
	if err := safehttp.ValidateURL(req.URL, s.outboundAllow); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateWebhookHeaders(req.Headers); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	active := req.Active == nil || *req.Active
	if req.Secret == "" {
		req.Secret = generateWebhookSecret()
	}

	var h Webhook
	var headers string
	err := s.db.Pool.QueryRow(r.Context(), s.q.CreateWebhook,
		t.ID, req.Name, req.URL, req.Secret, s.sealWebhookHeaders(req.Headers), active).
		Scan(&h.ID, &h.TenantID, &h.Name, &h.URL, &h.Secret, &headers, &h.Active, &h.CreatedAt, &h.UpdatedAt)
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "insert webhook failed", err)
		return
	}
	h.HeaderNames = s.webhookHeaderNames(headers)
	s.invalidateWebhooks(r.Context(), t.ID)
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
		h, err := s.scanWebhook(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, h)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req updateWebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Name != nil {
		if *req.Name = strings.TrimSpace(*req.Name); *req.Name == "" {
			writeError(w, http.StatusBadRequest, "name can't be empty")
			return
		}
	}
	if req.URL != nil {
		if err := safehttp.ValidateURL(*req.URL, s.outboundAllow); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	var headers *string
	if req.Headers != nil {
		if err := validateWebhookHeaders(*req.Headers); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		v := s.sealWebhookHeaders(*req.Headers)
		headers = &v
	}
	h, err := s.scanWebhook(s.db.Pool.QueryRow(r.Context(), s.q.UpdateWebhook,
		id, t.ID, req.Name, req.URL, headers, req.Active))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "webhook not found")
		return
	}
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "update webhook failed", err)
		return
	}
	s.invalidateWebhooks(r.Context(), t.ID)
	writeJSON(w, http.StatusOK, h)
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
	s.invalidateWebhooks(r.Context(), t.ID)
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
