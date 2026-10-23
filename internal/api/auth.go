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
	agentCtxKey
	scopeCtxKey
)

// API key scopes (api_keys.scope). Console sessions always act as scopeFull.
const (
	scopeFull  = "full"
	scopeCalls = "calls"
)

type Tenant struct {
	ID               uuid.UUID `json:"id"`
	Name             string    `json:"name"`
	SipDomain        string    `json:"sip_domain"`
	OutboundStrategy string    `json:"outbound_strategy"`
	FailoverCauses   []string  `json:"failover_causes"`
	// HoldMusic is the tenant's system hold music (an audio asset's file_path, or
	// "" for the built-in default). Not populated during auth resolution — only
	// by the tenant settings handlers (handleGetMyTenant / handlePatchMyTenant).
	HoldMusic string `json:"hold_music"`
	// RecordCalls toggles recording a call once the agent and client connect.
	// Like HoldMusic, populated only by the tenant settings handlers.
	RecordCalls bool `json:"record_calls"`
	// MaxCallSeconds is the tenant's max call duration (also the ephemeral TURN
	// credential lifetime). Populated only by the tenant settings handlers.
	MaxCallSeconds int `json:"max_call_seconds"`
	// TransferFailedMessage is the copy played to a caller when a blind transfer
	// finds no one available. Populated only by the tenant settings handlers.
	TransferFailedMessage string `json:"transfer_failed_message"`
	// RecordingSampleRate is the sample rate (Hz) the finished call recording is
	// encoded at. Populated only by the tenant settings handlers.
	RecordingSampleRate int `json:"recording_sample_rate"`
	// RecordingFormat is the finished recording's format: ogg, mp3 or wav.
	// Populated only by the tenant settings handlers.
	RecordingFormat string `json:"recording_format"`
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
		scope := scopeFull
		if strings.HasPrefix(token, "tk_") {
			// Tenant API key: look up its hash; reject if unknown/revoked.
			id, keyScope, ok := s.tenantForAPIKey(r.Context(), token)
			if !ok {
				writeError(w, http.StatusUnauthorized, "invalid credentials")
				return
			}
			tenantID, scope = id, keyScope
		} else {
			claims, err := s.parseConsoleToken(token)
			if err != nil || !s.consoleUserExists(r.Context(), claims) {
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
		ctx = context.WithValue(ctx, scopeCtxKey, scope)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireFullScope admits only full-access callers (console sessions and
// 'full' API keys) to the admin routes it wraps; a 'calls' key gets 403.
// Mount it inside the AuthRequired group.
func (s *Server) RequireFullScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if scope, _ := r.Context().Value(scopeCtxKey).(string); scope != scopeFull {
			writeError(w, http.StatusForbidden, "this API key's scope does not allow this endpoint")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// consoleUserExists reports whether a console token's user still exists in
// its tenant. Console JWTs are stateless, so without this a deleted user's
// session would stay valid until expiry. Fails closed on a DB error.
func (s *Server) consoleUserExists(ctx context.Context, c *ConsoleClaims) bool {
	userID, err := uuid.Parse(c.Subject)
	if err != nil {
		return false
	}
	var ok bool
	if err := s.db.Pool.QueryRow(ctx, s.q.UserExists, userID, c.TenantID).Scan(&ok); err != nil {
		return false
	}
	return ok
}

// tenantForAPIKey resolves a presented API key to its tenant and scope by
// hash, rejecting unknown or revoked keys. On success it best-effort stamps
// last_used_at (fire-and-forget, own context) so a slow write never delays
// the request.
func (s *Server) tenantForAPIKey(ctx context.Context, plaintext string) (uuid.UUID, string, bool) {
	hash := HashKey(plaintext)
	var (
		tenantID uuid.UUID
		scope    string
	)
	if err := s.db.Pool.QueryRow(ctx, s.q.ResolveAPIKeyTenant, hash).Scan(&tenantID, &scope); err != nil {
		return uuid.Nil, "", false
	}
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = s.db.Pool.Exec(bg, s.q.TouchAPIKey, hash)
	}()
	return tenantID, scope, true
}

func tenantFromCtx(ctx context.Context) *Tenant {
	if t, ok := ctx.Value(tenantCtxKey).(*Tenant); ok {
		return t
	}
	return nil
}

// AgentAuthRequired validates the agent JWT (aud tring.sdk) and stashes the
// claims. This is a NARROW surface: the agent SDK uses it to control its OWN
// active call (hold; later transfer). Unlike origination — which stays
// backend-only (ADR-001) — self call-control is a safe agent capability: the
// JWT scopes every action to that one agent's current call.
func (s *Server) AgentAuthRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := s.ParseAgentToken(bearerToken(r))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid agent token")
			return
		}
		ctx := context.WithValue(r.Context(), agentCtxKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func agentFromCtx(ctx context.Context) *AgentClaims {
	if c, ok := ctx.Value(agentCtxKey).(*AgentClaims); ok {
		return c
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
