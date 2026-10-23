package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// apiKeyResponse is the tenant-facing shape of an API key. The plaintext Key
// is populated ONLY in the create response (shown once); list never returns
// it (or the hash).
type apiKeyResponse struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Key        string     `json:"key,omitempty"` // plaintext — create response only
	KeyPrefix  string     `json:"key_prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	Revoked    bool       `json:"revoked"`
}

type createAPIKeyRequest struct {
	Name string `json:"name"`
}

// handleCreateAPIKey issues a new tenant API key. The plaintext is returned
// exactly once here; only its SHA-256 hash is stored.
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req createAPIKeyRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "API key"
	}

	plaintext, hash, prefix, err := GenerateKey("live")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "key generation failed")
		return
	}

	var (
		id        uuid.UUID
		createdAt time.Time
	)
	if err := s.db.Pool.QueryRow(r.Context(), s.q.CreateAPIKey,
		t.ID, name, hash, prefix,
	).Scan(&id, &createdAt); err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "create failed", err)
		return
	}

	writeJSON(w, http.StatusCreated, apiKeyResponse{
		ID:        id,
		Name:      name,
		Key:       plaintext, // shown once
		KeyPrefix: prefix,
		CreatedAt: createdAt,
	})
}

// handleListAPIKeys lists the tenant's keys (prefix + status only, never the
// plaintext or hash).
func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	rows, err := s.db.Pool.Query(r.Context(), s.q.ListAPIKeys, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	out := []apiKeyResponse{}
	for rows.Next() {
		var (
			k         apiKeyResponse
			revokedAt *time.Time
		)
		if err := rows.Scan(&k.ID, &k.Name, &k.KeyPrefix, &k.CreatedAt, &k.LastUsedAt, &revokedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		k.Revoked = revokedAt != nil
		out = append(out, k)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRevokeAPIKey soft-revokes a key (scoped to the tenant). 404 if it
// doesn't exist or is already revoked.
func (s *Server) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	tag, err := s.db.Pool.Exec(r.Context(), s.q.RevokeAPIKey, id, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "revoke failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "key not found or already revoked")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
