package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

type ctxKey int

const (
	tenantCtxKey ctxKey = iota
	agentCtxKey
)

type Tenant struct {
	ID               uuid.UUID `json:"id"`
	Name             string    `json:"name"`
	SipDomain        string    `json:"sip_domain"`
	OutboundStrategy string    `json:"outbound_strategy"`
	FailoverCauses   []string  `json:"failover_causes"`
}

func (s *Server) AuthRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := bearerToken(r)
		if key == "" {
			writeError(w, http.StatusUnauthorized, "missing API key")
			return
		}
		var t Tenant
		err := s.db.Pool.QueryRow(r.Context(),
			`SELECT id, name, sip_domain, outbound_strategy, failover_causes
			 FROM tenants WHERE api_key_hash = $1`,
			HashKey(key),
		).Scan(&t.ID, &t.Name, &t.SipDomain, &t.OutboundStrategy, &t.FailoverCauses)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}
		ctx := context.WithValue(r.Context(), tenantCtxKey, &t)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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

// JWTAuthRequired validates a Bearer JWT and stashes the decoded claims in
// the request context. Used by /v1/sdk/* routes (browser-side traffic).
func (s *Server) JWTAuthRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := bearerToken(r)
		if raw == "" {
			writeError(w, http.StatusUnauthorized, "missing token")
			return
		}
		claims, err := s.ParseAgentToken(raw)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid token: "+err.Error())
			return
		}
		ctx := context.WithValue(r.Context(), agentCtxKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func agentClaimsFromCtx(ctx context.Context) *AgentClaims {
	if c, ok := ctx.Value(agentCtxKey).(*AgentClaims); ok {
		return c
	}
	return nil
}
