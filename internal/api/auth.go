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
	consoleCtxKey   // *ConsoleClaims, set by ConsoleAuthRequired
	principalCtxKey // *Principal, set for console sessions
)

// API key scopes (api_keys.scope). Admin console sessions act as scopeFull,
// other console sessions as scopeSelf (principal.go).
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
	// TransferTimeoutSeconds is how long an agent's transfer rings before the
	// call goes back to them. Populated only by the tenant settings handlers.
	TransferTimeoutSeconds int `json:"transfer_timeout_seconds"`
	// RecordingSampleRate is the sample rate (Hz) the finished call recording is
	// encoded at. Populated only by the tenant settings handlers.
	RecordingSampleRate int `json:"recording_sample_rate"`
	// RecordingFormat is the finished recording's format: ogg, mp3 or wav.
	// Populated only by the tenant settings handlers.
	RecordingFormat string `json:"recording_format"`
	// MaskNumbers* mask client phone numbers for each non-admin role
	// (internal/mask). Populated only by the tenant settings handlers.
	MaskNumbersAgents      bool `json:"mask_numbers_agents"`
	MaskNumbersManagers    bool `json:"mask_numbers_managers"`
	MaskNumbersSupervisors bool `json:"mask_numbers_supervisors"`
}

// AuthRequired gates all /v1/* tenant-scoped routes on either a tenant API
// key (`tk_live_…`, for server-to-server CRM backends) or a console JWT
// issued by /v1/console/login (for the admin console). Both resolve to the
// owning tenant, stashed in the request context. A console session also
// stashes its Principal, reloaded from the DB here on every request.
func (s *Server) AuthRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing credentials")
			return
		}

		tenantID, scope, principal, ok := s.resolveCredentials(r.Context(), token)
		if !ok {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
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
		if principal != nil {
			ctx = context.WithValue(ctx, principalCtxKey, principal)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// resolveCredentials checks a bearer token the way AuthRequired does: a tenant
// API key must still exist unrevoked; a console JWT must be unexpired and its
// user still present with a current session epoch. It returns the tenant, the
// scope and (for a console session) the freshly loaded Principal. A long-lived
// stream calls it again to re-check the credentials it was opened with.
func (s *Server) resolveCredentials(ctx context.Context, token string) (uuid.UUID, string, *Principal, bool) {
	if strings.HasPrefix(token, "tk_") {
		id, scope, ok := s.tenantForAPIKey(ctx, token)
		return id, scope, nil, ok
	}
	claims, err := s.parseConsoleToken(token)
	if err != nil {
		return uuid.Nil, "", nil, false
	}
	p, err := s.loadPrincipal(ctx, claims)
	if err != nil {
		return uuid.Nil, "", nil, false
	}
	scope := scopeFull
	if !p.IsAdmin() {
		scope = scopeSelf
	}
	return claims.TenantID, scope, p, true
}

// RequireFullScope admits only full-access callers (admin console sessions
// and 'full' API keys) to the admin routes it wraps; a 'calls' key or a
// non-admin console session gets 403.
// Mount it inside the AuthRequired group.
func (s *Server) RequireFullScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if scope, _ := r.Context().Value(scopeCtxKey).(string); scope != scopeFull {
			writeError(w, http.StatusForbidden, "not permitted")
			return
		}
		next.ServeHTTP(w, r)
	})
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
