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
	var got uuid.UUID
	if clearLogin {
		err = s.db.Pool.QueryRow(ctx, s.q.ClearUserLogin, p.TenantID, id).Scan(&got)
	} else {
		err = s.db.Pool.QueryRow(ctx, s.q.UpdateUser, p.TenantID, id, req.DisplayName, req.Email, req.Role, hash).Scan(&got)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "email already in use")
			return
		}
		writeError(w, http.StatusInternalServerError, "update user failed")
		return
	}
	u, err := s.getUserRow(ctx, p.TenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load user failed")
		return
	}
	writeJSON(w, http.StatusOK, u)
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
