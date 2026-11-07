package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/shridarpatil/tring/internal/callrow"
)

// The /v1/me handlers serve the logged-in user's own view. Each acts only on
// the Principal's user id, never on an id from the request.

const (
	minPasswordLen = 8
	meCallsDefault = 25
	meCallsMax     = 100
)

func (s *Server) handleGetMe(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	writeJSON(w, http.StatusOK, newMeUser(p, p.Email))
}

// handleMeToken mints the caller's own agent JWT so the agent view can open
// the gateway socket without a CRM backend in the loop.
func (s *Server) handleMeToken(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	resp, err := s.agentTokenResponse(r.Context(), tenantFromCtx(r.Context()), p.UserID, p.SipUsername)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "issue token")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleMeAvailability(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	var req setAgentAvailabilityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	a, err := s.applyAgentAvailability(r.Context(), p.TenantID, p.UserID, req.Available)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

type mePasswordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

func (s *Server) handleMePassword(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	var req mePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.New) < minPasswordLen {
		writeError(w, http.StatusBadRequest, "new password must be at least 8 characters")
		return
	}
	var hash string
	if err := s.db.Pool.QueryRow(r.Context(), s.q.GetUserPasswordHash, p.TenantID, p.UserID).Scan(&hash); err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Current)) != nil {
		writeError(w, http.StatusUnauthorized, "current password is wrong")
		return
	}
	// bcrypt rejects inputs over 72 bytes; surface that as a bad request.
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.New), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid new password")
		return
	}
	// The epoch bump revokes every session, this one included, so hand back a
	// token for the new epoch to keep the caller signed in.
	var epoch int
	if err := s.db.Pool.QueryRow(r.Context(), s.q.SetUserPassword, p.TenantID, p.UserID, string(newHash)).Scan(&epoch); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	tok, exp, err := s.IssueConsoleToken(p.UserID, p.TenantID, p.Email, p.Role, epoch, consoleTokenTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "issue token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expires_at": exp})
}

type meStats struct {
	Taken          int `json:"taken"`
	Missed         int `json:"missed"`
	TalkSeconds    int `json:"talk_seconds"`
	AvgTalkSeconds int `json:"avg_talk_seconds"`
}

func (s *Server) handleMeStats(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	var q string
	switch r.URL.Query().Get("range") {
	case "", "today":
		q = s.q.GetMeStatsToday
	case "7d":
		q = s.q.GetMeStats7d
	default:
		writeError(w, http.StatusBadRequest, "range must be today or 7d")
		return
	}
	var st meStats
	if err := s.db.Pool.QueryRow(r.Context(), q, p.TenantID, p.UserID).
		Scan(&st.Taken, &st.TalkSeconds, &st.Missed); err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if st.Taken > 0 {
		st.AvgTalkSeconds = st.TalkSeconds / st.Taken
	}
	writeJSON(w, http.StatusOK, st)
}

// myCall is a /v1/me/calls item: the call plus whether it is one the user
// missed (it rang them, they never answered, it ended) rather than took.
type myCall struct {
	Call
	Missed bool `json:"missed,omitempty"`
}

func (s *Server) handleMeCalls(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	// Days before today's local midnight: "7d" is today and the 6 days before.
	var days int
	switch r.URL.Query().Get("range") {
	case "", "today":
		days = 0
	case "7d":
		days = 6
	case "30d":
		days = 29
	default:
		writeError(w, http.StatusBadRequest, "range must be today, 7d or 30d")
		return
	}
	limit, offset := meCallsDefault, 0
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = min(n, meCallsMax)
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && n > 0 {
		offset = n
	}
	var total int
	if err := s.db.Pool.QueryRow(r.Context(), s.q.CountMyCalls, p.TenantID, p.UserID, days).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "count failed")
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), s.q.ListMyCalls, p.TenantID, p.UserID, limit, offset, days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	out := []myCall{}
	for rows.Next() {
		var c myCall
		if c.Call, err = callrow.ScanWith(rows, &c.Missed); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		switch {
		case c.Missed:
			// Recordings are only for calls the user took (is-my-call).
			c.RecordingURL = nil
		case c.RecordingURL != nil:
			// Point at the /me route: the tenant-wide one rejects this session.
			u := "/v1/me/calls/" + c.ID.String() + "/recording"
			c.RecordingURL = &u
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, pageEnvelope{Items: out, Total: total, Limit: limit, Offset: offset})
}

func (s *Server) handleMeCallRecording(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var mine bool
	err = s.db.Pool.QueryRow(r.Context(), s.q.IsMyCall, p.TenantID, id, p.UserID).Scan(&mine)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if !mine {
		writeError(w, http.StatusNotFound, "call not found")
		return
	}
	s.serveCallRecording(w, r, p.TenantID, id)
}
