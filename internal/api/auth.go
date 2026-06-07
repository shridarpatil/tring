package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ctxKey int

const (
	tenantCtxKey ctxKey = iota
)

type Tenant struct {
	ID               uuid.UUID `json:"id"`
	Name             string    `json:"name"`
	SipDomain        string    `json:"sip_domain"`
	OutboundStrategy string    `json:"outbound_strategy"`
	FailoverCauses   []string  `json:"failover_causes"`
	// HoldMusic is the tenant's system hold music (an audio asset's fs_path, or
	// "" for the built-in default). Not populated during auth resolution — only
	// by the tenant settings handlers (handleGetMyTenant / handlePatchMyTenant).
	HoldMusic string `json:"hold_music"`
	// RecordCalls toggles recording a call once the agent and client connect.
	// Like HoldMusic, populated only by the tenant settings handlers.
	RecordCalls bool `json:"record_calls"`
	// MaxCallSeconds is the tenant's max call duration (also the ephemeral TURN
	// credential lifetime). Populated only by the tenant settings handlers.
	MaxCallSeconds int `json:"max_call_seconds"`
}

// AuthRequired gates all /v1/* tenant-scoped routes on either a tenant API
// key (`tk_live_…`, for server-to-server CRM backends) or a console JWT
// issued by /v1/console/login (for the admin console). Both resolve to the
// owning tenant, stashed in the request context.
func (s *Server) AuthRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing credentials")
			return
		}

		var tenantID uuid.UUID
		if strings.HasPrefix(token, "tk_") {
			// Tenant API key: look up its hash; reject if unknown/revoked.
			id, ok := s.tenantForAPIKey(r.Context(), token)
			if !ok {
				writeError(w, http.StatusUnauthorized, "invalid credentials")
				return
			}
			tenantID = id
		} else {
			claims, err := s.parseConsoleToken(token)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid credentials")
				return
			}
			tenantID = claims.TenantID
		}

		var t Tenant
		err := s.db.Pool.QueryRow(r.Context(), s.q.GetTenant, tenantID).
			Scan(&t.ID, &t.Name, &t.SipDomain, &t.OutboundStrategy, &t.FailoverCauses)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		ctx := context.WithValue(r.Context(), tenantCtxKey, &t)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// tenantForAPIKey resolves a presented API key to its tenant by hash,
// rejecting unknown or revoked keys. On success it best-effort stamps
// last_used_at (fire-and-forget, own context) so a slow write never delays
// the request.
func (s *Server) tenantForAPIKey(ctx context.Context, plaintext string) (uuid.UUID, bool) {
	hash := HashKey(plaintext)
	var tenantID uuid.UUID
	if err := s.db.Pool.QueryRow(ctx, s.q.ResolveAPIKeyTenant, hash).Scan(&tenantID); err != nil {
		return uuid.Nil, false
	}
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = s.db.Pool.Exec(bg, s.q.TouchAPIKey, hash)
	}()
	return tenantID, true
}

func tenantFromCtx(ctx context.Context) *Tenant {
	if t, ok := ctx.Value(tenantCtxKey).(*Tenant); ok {
		return t
	}
	return nil
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.Header.Get("X-API-Key")
}

// NOTE: the agent JWT is validated by the media gateway (internal/gateway) for
// the WebSocket — the API has no agent-JWT-scoped routes. The SDK never calls
// the Tring API; outbound origination is backend-only (POST /v1/calls).
