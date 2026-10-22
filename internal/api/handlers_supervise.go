package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// supervisorTokenTTL bounds how long a minted supervisor RTC token is valid —
// long enough to open the /rtc socket, short enough that a leak expires fast.
const supervisorTokenTTL = 2 * time.Minute

// consoleCtxKey stashes *ConsoleClaims for ConsoleAuthRequired-gated routes.
// tenantCtxKey=0 and agentCtxKey=1 already exist in auth.go.
const consoleCtxKey ctxKey = 2

// ConsoleAuthRequired gates a route on a CONSOLE JWT specifically (not an API
// key): the supervise permission is per-user, and an API key has no user
// identity. It stashes the console claims (user id + tenant) for the handler.
func (s *Server) ConsoleAuthRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" || strings.HasPrefix(token, "tk_") {
			writeError(w, http.StatusUnauthorized, "console session required")
			return
		}
		claims, err := s.parseConsoleToken(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid console session")
			return
		}
		ctx := context.WithValue(r.Context(), consoleCtxKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func consoleFromCtx(ctx context.Context) *ConsoleClaims {
	if c, ok := ctx.Value(consoleCtxKey).(*ConsoleClaims); ok {
		return c
	}
	return nil
}

type superviseResponse struct {
	GatewayURL string      `json:"gateway_url"`
	Room       string      `json:"room"`
	Token      string      `json:"token"`
	ICEServers []iceServer `json:"ice_servers"`
}

// handleSupervise mints a supervisor RTC token for a live call with a media
// room — web, phone or WhatsApp. The console operator must have can_supervise;
// the call must be live (ringing/answered) with a resolvable pion room (calls
// placed over ESL with POST /calls have none). The returned token pins that room.
func (s *Server) handleSupervise(w http.ResponseWriter, r *http.Request) {
	claims := consoleFromCtx(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no console session")
		return
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "bad user id")
		return
	}
	callID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	// Permission gate.
	var canSupervise bool
	err = s.db.Pool.QueryRow(r.Context(), s.q.GetUserSupervise, userID, claims.TenantID).Scan(&canSupervise)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !canSupervise) {
		writeError(w, http.StatusForbidden, "not permitted to supervise")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "permission lookup failed")
		return
	}

	// Resolve the live call → pion room.
	var roomID, status string
	err = s.db.Pool.QueryRow(r.Context(), s.q.GetSuperviseTarget, callID, claims.TenantID).Scan(&roomID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "call not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "call lookup failed")
		return
	}
	if status != "answered" && status != "ringing" {
		writeError(w, http.StatusConflict, "call is not live")
		return
	}
	if roomID == "" {
		writeError(w, http.StatusConflict, "call has no media room")
		return
	}

	// The pion room id is "<tenant>/<name>"; the token pins the NAME (the gateway
	// re-prefixes the tenant). Strip the tenant prefix.
	roomName := strings.TrimPrefix(roomID, claims.TenantID.String()+"/")

	token, _, err := s.IssueSupervisorToken(claims.TenantID, roomName, userID.String(), claims.Email, supervisorTokenTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token issue failed")
		return
	}
	writeJSON(w, http.StatusOK, superviseResponse{
		GatewayURL: s.gatewayPublicURL,
		Room:       roomName,
		Token:      token,
		ICEServers: s.iceServersFor(claims.Email, s.turnTTLFor(r.Context(), claims.TenantID)),
	})
}
