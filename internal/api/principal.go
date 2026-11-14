package api

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/google/uuid"
)

// scopeSelf is the scope of a non-admin console session. Every pre-existing
// route rejects it (RequireFullScope / RejectSelfScope), so such a session can
// only reach routes that check the Principal themselves: default-deny.
const scopeSelf = "self"

// Principal is the console user behind a request, loaded from the DB on every
// request so a role or team change (or a revoked login) applies immediately;
// the JWT's role claim is only a UI hint.
type Principal struct {
	UserID, TenantID uuid.UUID
	Role             string // "admin" | "supervisor" | "agent"
	SipUsername      string
	Email            string
	DisplayName      string
	Teams            []string // team names the user is a member of
	ManagedTeams     []string // subset where the user is the team's manager
	Epoch            int      // users.session_epoch; tokens from older epochs are revoked
}

// IsAdmin reports whether the user administers the tenant.
func (p *Principal) IsAdmin() bool { return p.Role == "admin" }

// SeesAllTeams reports whether the user's role spans every team in the tenant.
func (p *Principal) SeesAllTeams() bool { return p.Role == "admin" || p.Role == "supervisor" }

// CanViewTeam reports whether the user may see a team's live state.
func (p *Principal) CanViewTeam(team string) bool {
	return p.SeesAllTeams() || slices.Contains(p.Teams, team)
}

// ManagesTeam reports whether the user may act on a team (e.g. supervise it).
func (p *Principal) ManagesTeam(team string) bool {
	return p.SeesAllTeams() || slices.Contains(p.ManagedTeams, team)
}

// SupervisableTeams returns all=true when the user may supervise any team,
// otherwise the teams they manage.
func (p *Principal) SupervisableTeams() (all bool, teams []string) {
	if p.SeesAllTeams() {
		return true, nil
	}
	return false, p.ManagedTeams
}

func principalFromCtx(ctx context.Context) *Principal {
	if p, ok := ctx.Value(principalCtxKey).(*Principal); ok {
		return p
	}
	return nil
}

// loadPrincipal resolves a console token's user. Console JWTs are stateless,
// so this is also the revocation check: a deleted user, one moved out of the
// tenant, or one whose login was removed (password_hash NULL) gets
// pgx.ErrNoRows, and a token minted before the user's last credential change
// (session_epoch bump) gets errStaleSession. Callers fail closed on any error.
func (s *Server) loadPrincipal(ctx context.Context, c *ConsoleClaims) (*Principal, error) {
	userID, err := uuid.Parse(c.Subject)
	if err != nil {
		return nil, err
	}
	p, err := s.principalFor(ctx, userID, c.TenantID)
	if err != nil {
		return nil, err
	}
	if p.Epoch != c.Epoch {
		return nil, errStaleSession
	}
	return p, nil
}

var errStaleSession = errors.New("console session revoked")

func (s *Server) principalFor(ctx context.Context, userID, tenantID uuid.UUID) (*Principal, error) {
	p := &Principal{UserID: userID, TenantID: tenantID}
	err := s.db.Pool.QueryRow(ctx, s.q.GetConsolePrincipal, userID, tenantID).
		Scan(&p.Role, &p.SipUsername, &p.Email, &p.DisplayName, &p.Teams, &p.ManagedTeams, &p.Epoch)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// RejectSelfScope keeps non-admin console sessions out of the routes it wraps.
// API keys of any scope pass; RequireFullScope handles those.
func (s *Server) RejectSelfScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if scope, _ := r.Context().Value(scopeCtxKey).(string); scope == scopeSelf {
			writeError(w, http.StatusForbidden, "not permitted")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AllowDashboardStream guards the tenant-wide dashboard stream: like
// RejectSelfScope, plus supervisors, whose role spans the whole tenant. It
// wraps that one route only; supervisors stay out of every other admin route.
func (s *Server) AllowDashboardStream(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scope, _ := r.Context().Value(scopeCtxKey).(string)
		if !mayStreamDashboard(scope, principalFromCtx(r.Context())) {
			writeError(w, http.StatusForbidden, "not permitted")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mayStreamDashboard reports whether a caller may hold the tenant-wide
// dashboard stream: full-scope callers and supervisors.
func mayStreamDashboard(scope string, p *Principal) bool {
	return scope != scopeSelf || (p != nil && p.Role == "supervisor")
}

// RequirePrincipal admits only console sessions (API keys have no user), for
// routes that authorize by the Principal. Mount it inside AuthRequired.
func (s *Server) RequirePrincipal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principalFromCtx(r.Context()) == nil {
			writeError(w, http.StatusUnauthorized, "console session required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
