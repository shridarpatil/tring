package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/shridarpatil/tring/internal/callcdr"
	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/presence"
)

// Admin management of a tenant's users: roles, console logins and SIP
// identities. Mounted behind RequireFullScope + RequirePrincipal.

var userRoles = []string{"admin", "supervisor", "agent"}

type userRow struct {
	ID                uuid.UUID   `json:"id"`
	DisplayName       string      `json:"display_name"`
	Email             string      `json:"email"`
	Role              string      `json:"role"`
	SipUsername       string      `json:"sip_username"`
	AvailableForCalls bool        `json:"available_for_calls"`
	Teams             []myTeamRef `json:"teams"`
	CanLogin          bool        `json:"can_login"`
	CreatedAt         time.Time   `json:"created_at"`
}

type userScanner interface{ Scan(dest ...any) error }

func scanUserRow(row userScanner) (userRow, error) {
	var (
		u        userRow
		teams    []string
		managers []string
	)
	if err := row.Scan(&u.ID, &u.DisplayName, &u.Email, &u.Role, &u.SipUsername, &u.AvailableForCalls,
		&teams, &managers, &u.CanLogin, &u.CreatedAt); err != nil {
		return u, err
	}
	u.Teams = make([]myTeamRef, 0, len(teams))
	for _, name := range teams {
		u.Teams = append(u.Teams, myTeamRef{Name: name, IsManager: slices.Contains(managers, name)})
	}
	return u, nil
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	rows, err := s.db.Pool.Query(r.Context(), s.q.ListUsers, p.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list users failed")
		return
	}
	defer rows.Close()
	out := []userRow{}
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list users failed")
			return
		}
		out = append(out, u)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusInternalServerError, "list users failed")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getUserRow(ctx context.Context, tenantID, id uuid.UUID) (userRow, error) {
	return scanUserRow(s.db.Pool.QueryRow(ctx, s.q.GetUser, tenantID, id))
}

type createUserRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
	SipUsername string `json:"sip_username"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !strings.Contains(email, "@") {
		writeError(w, http.StatusBadRequest, "valid email required")
		return
	}
	if len(req.Password) < minPasswordLen {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if !slices.Contains(userRoles, req.Role) {
		writeError(w, http.StatusBadRequest, "role must be admin, supervisor or agent")
		return
	}
	sip := strings.ToLower(strings.TrimSpace(req.SipUsername))
	if sip == "" {
		sip = email
	}
	if !validSipUsername(sip) {
		writeError(w, http.StatusBadRequest, "sip_username may only contain letters, digits and . _ @ + - (max 64)")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash password")
		return
	}
	sipPassword, err := GenerateSipPassword()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generate password")
		return
	}
	var id uuid.UUID
	err = s.db.Pool.QueryRow(r.Context(), s.q.CreateUser,
		p.TenantID, email, string(hash), sip, s.sealSecret(sipPassword), req.DisplayName, req.Role).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "email or sip_username already in use")
			return
		}
		writeError(w, http.StatusInternalServerError, "create user failed")
		return
	}
	u, err := s.getUserRow(r.Context(), p.TenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load user failed")
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

type patchUserRequest struct {
	DisplayName *string `json:"display_name"`
	Email       *string `json:"email"`
	Role        *string `json:"role"`
	Password    *string `json:"password"`
	SipUsername *string `json:"sip_username"`
}

// flowRef names a flow for the console to link to.
type flowRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// patchUserResponse is the updated row plus, after a SIP rename, the flows
// still pinned to the old name (transfer agent_sip) for the admin to fix.
type patchUserResponse struct {
	userRow
	FlowsReferencingOld []flowRef `json:"flows_referencing_old,omitempty"`
}

func (s *Server) handlePatchUser(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req patchUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	clearLogin := false
	if req.Email != nil {
		e := strings.ToLower(strings.TrimSpace(*req.Email))
		req.Email = &e
		if e == "" {
			clearLogin = true
		} else if !strings.Contains(e, "@") {
			writeError(w, http.StatusBadRequest, "valid email required")
			return
		}
	}
	if req.Role != nil && !slices.Contains(userRoles, *req.Role) {
		writeError(w, http.StatusBadRequest, "role must be admin, supervisor or agent")
		return
	}
	if req.SipUsername != nil {
		sip := strings.ToLower(strings.TrimSpace(*req.SipUsername))
		if !validSipUsername(sip) {
			writeError(w, http.StatusBadRequest, "sip_username may only contain letters, digits and . _ @ + - (max 64)")
			return
		}
		req.SipUsername = &sip
	}
	var hash *string
	if req.Password != nil {
		if clearLogin {
			writeError(w, http.StatusBadRequest, "cannot set a password while clearing the login")
			return
		}
		if len(*req.Password) < minPasswordLen {
			writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}
		b, err := bcrypt.GenerateFromPassword([]byte(*req.Password), bcrypt.DefaultCost)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "hash password")
			return
		}
		h := string(b)
		hash = &h
	}
	// An admin who demotes or de-logins themselves could leave the tenant
	// with nobody able to manage it.
	if id == p.UserID && (clearLogin || (req.Role != nil && *req.Role != p.Role)) {
		writeError(w, http.StatusBadRequest, "you cannot change your own role or remove your own login")
		return
	}

	ctx := r.Context()
	// A password on a user with no email would leave a half-set login that
	// can't sign in (require the email to exist or be set in the same request),
	// and a rename needs the old name.
	var cur userRow
	if (hash != nil && req.Email == nil) || req.SipUsername != nil {
		cur, err = s.getUserRow(ctx, p.TenantID, id)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "load user failed")
			return
		}
		if hash != nil && req.Email == nil && cur.Email == "" {
			writeError(w, http.StatusBadRequest, "set an email to give this user a login")
			return
		}
	}
	oldSip := ""
	if req.SipUsername != nil && *req.SipUsername != cur.SipUsername {
		oldSip = cur.SipUsername
		// Mid-call the gateway holds the agent's legs under the old name; a
		// rename would orphan them (on-call count, CDR attribution, transfer).
		onCall, err := s.userOnCall(ctx, p.TenantID, id, oldSip)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "update user failed")
			return
		}
		if onCall {
			writeError(w, http.StatusConflict, "user is on a call")
			return
		}
	}

	if code, msg := s.updateUser(ctx, p.TenantID, id, req, hash, clearLogin, oldSip != ""); code != 0 {
		writeError(w, code, msg)
		return
	}
	u, err := s.getUserRow(ctx, p.TenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load user failed")
		return
	}
	resp := patchUserResponse{userRow: u}
	if oldSip != "" {
		resp.FlowsReferencingOld = s.afterSipRename(ctx, p.TenantID, u, oldSip)
	} else if req.DisplayName != nil {
		s.invalidateAgentCache(ctx, p.TenantID, u.SipUsername) // CDR agent label
	}
	writeJSON(w, http.StatusOK, resp)
}

// updateUser applies a PATCH in one transaction, so a rename that collides
// leaves the other fields unchanged too. Returns 0 or the status + message.
func (s *Server) updateUser(ctx context.Context, tenantID, id uuid.UUID, req patchUserRequest, hash *string, clearLogin, rename bool) (int, string) {
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return http.StatusInternalServerError, "update user failed"
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var got uuid.UUID
	if clearLogin {
		err = tx.QueryRow(ctx, s.q.ClearUserLogin, tenantID, id).Scan(&got)
	} else {
		err = tx.QueryRow(ctx, s.q.UpdateUser, tenantID, id, req.DisplayName, req.Email, req.Role, hash).Scan(&got)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return http.StatusNotFound, "user not found"
	}
	if isUniqueViolation(err) {
		return http.StatusConflict, "email already in use"
	}
	if err != nil {
		return http.StatusInternalServerError, "update user failed"
	}
	if rename {
		_, err = tx.Exec(ctx, s.q.RenameUserSip, tenantID, id, *req.SipUsername)
		if isUniqueViolation(err) {
			return http.StatusConflict, "sip_username already in use"
		}
		if err != nil {
			return http.StatusInternalServerError, "update user failed"
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return http.StatusInternalServerError, "update user failed"
	}
	return 0, ""
}

// userOnCall reports whether the user is on a live call: an open answered CDR,
// or the gateway's on_call presence marker (set before the CDR is answered).
func (s *Server) userOnCall(ctx context.Context, tenantID, id uuid.UUID, sip string) (bool, error) {
	if s.presence.Status(ctx, tenantID.String(), sip) == presence.StatusOnCall {
		return true, nil
	}
	var on bool
	err := s.db.Pool.QueryRow(ctx, s.q.UserOnCall, tenantID, id).Scan(&on)
	return on, err
}

// afterSipRename retires the old SIP username everywhere outside the users
// row: the gateway's caches and presence keys, the agent's live presence
// socket (kicked so the SDK reconnects under the new name), and dashboards.
// All best-effort — the rename is already committed. Returns the flows still
// pinned to the old name.
func (s *Server) afterSipRename(ctx context.Context, tenantID uuid.UUID, u userRow, oldSip string) []flowRef {
	tenant := tenantID.String()
	s.invalidateAgentCache(ctx, tenantID, oldSip)
	// The new name may hold a stale entry from a deleted user who had it.
	s.invalidateAgentCache(ctx, tenantID, u.SipUsername)
	s.presence.SetAvailability(ctx, tenant, u.SipUsername, u.AvailableForCalls)
	if s.gatewayControlURL != "" {
		if err := s.gatewayControl(ctx, tenantID, "/control/agent-kick", map[string]string{"sip": oldSip}); err != nil {
			// The socket stays up under the old name until it next drops.
			s.log.Warn("agent kick failed", "sip", oldSip, "err", err)
		}
	}
	// After the kick, so a presence refresh racing it can't leave a marker.
	s.presence.Forget(ctx, tenant, oldSip)
	s.events.Publish(ctx, tenant, events.TypeAgentPresence, map[string]any{
		"sip_username": oldSip,
		"status":       presence.StatusOffline,
	})
	s.events.Publish(ctx, tenant, events.TypeAgentStatus, map[string]any{
		"agent_id":            u.ID,
		"sip_username":        u.SipUsername,
		"available_for_calls": u.AvailableForCalls,
	})

	rows, err := s.db.Pool.Query(ctx, s.q.FlowsByAgentSip, tenantID, oldSip)
	if err != nil {
		s.log.Warn("list flows by agent sip failed", "err", err)
		return nil
	}
	refs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[flowRef])
	if err != nil {
		s.log.Warn("list flows by agent sip failed", "err", err)
	}
	return refs
}

// invalidateAgentCache drops the gateway's cached id + label for an agent's
// SIP username (callcdr), so the next CDR reads the current row.
func (s *Server) invalidateAgentCache(ctx context.Context, tenantID uuid.UUID, sip string) {
	if err := s.cache.Delete(ctx, callcdr.AgentCacheKey(tenantID, sip)); err != nil {
		s.log.Warn("agent cache invalidate failed", "sip", sip, "err", err)
	}
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if id == p.UserID {
		writeError(w, http.StatusBadRequest, "you cannot delete yourself")
		return
	}
	tag, err := s.db.Pool.Exec(r.Context(), s.q.DeleteUser, p.TenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete user failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
