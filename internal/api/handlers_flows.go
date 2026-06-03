package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/whatomate/tring/internal/orchestrator"
)

// flowListCacheTTL bounds staleness of the cached per-tenant flow list if an
// invalidation is ever missed. Matches the executor's definition-cache TTL.
const flowListCacheTTL = time.Hour

// flowListCacheKey is the cache key for a tenant's full flow list (GET
// /v1/flows). Per-tenant — invalidated on any flow create/update/delete.
func flowListCacheKey(tenantID uuid.UUID) string {
	return "flows:list:" + tenantID.String()
}

// invalidateFlowList drops a tenant's cached flow list so the next GET
// /v1/flows reflects a create/update/delete. Best-effort: a failed delete
// only means staleness until the TTL.
func (s *Server) invalidateFlowList(ctx context.Context, tenantID uuid.UUID) {
	if err := s.cache.Delete(ctx, flowListCacheKey(tenantID)); err != nil {
		log.Printf("flows: list cache invalidate (tenant %s) failed: %v", tenantID, err)
	}
}

// Flow mirrors the frontend's Flow shape (web/src/api/types.ts). The
// FreeSWITCH executor (Phase 3) consumes `definition` — an IVRFlowData
// v2 graph — to drive inbound calls.
type Flow struct {
	ID              uuid.UUID       `json:"id"`
	TenantID        uuid.UUID       `json:"tenant_id"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	IsActive        bool            `json:"is_active"`
	IsOutgoingEnd   bool            `json:"is_outgoing_end"`
	Definition      json.RawMessage `json:"definition"`
	WelcomeAudioURL string          `json:"welcome_audio_url"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type createFlowRequest struct {
	Name            string          `json:"name"`
	Description     string          `json:"description,omitempty"`
	IsActive        *bool           `json:"is_active,omitempty"`
	IsOutgoingEnd   *bool           `json:"is_outgoing_end,omitempty"`
	Definition      json.RawMessage `json:"definition,omitempty"`
	WelcomeAudioURL string          `json:"welcome_audio_url,omitempty"`
}

type patchFlowRequest struct {
	Name            *string         `json:"name,omitempty"`
	Description     *string         `json:"description,omitempty"`
	IsActive        *bool           `json:"is_active,omitempty"`
	IsOutgoingEnd   *bool           `json:"is_outgoing_end,omitempty"`
	Definition      json.RawMessage `json:"definition,omitempty"`
	WelcomeAudioURL *string         `json:"welcome_audio_url,omitempty"`
}

func scanFlow(scanner interface{ Scan(...interface{}) error }) (Flow, error) {
	var f Flow
	err := scanner.Scan(
		&f.ID, &f.TenantID, &f.Name, &f.Description,
		&f.IsActive, &f.IsOutgoingEnd,
		&f.Definition, &f.WelcomeAudioURL, &f.CreatedAt, &f.UpdatedAt,
	)
	return f, err
}

func (s *Server) handleCreateFlow(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req createFlowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	// Definition defaults to an empty v2 graph if not provided. JSONB
	// column default handles this but we pass NULL through so callers
	// can omit it without sending an empty object.
	var def interface{}
	if len(req.Definition) > 0 {
		def = req.Definition
	}

	row := s.db.Pool.QueryRow(r.Context(), s.q.CreateFlow,
		t.ID, req.Name, req.Description,
		req.IsActive, req.IsOutgoingEnd,
		def, req.WelcomeAudioURL,
	)
	f, err := scanFlow(row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "insert failed: "+err.Error())
		return
	}
	s.invalidateFlowList(r.Context(), t.ID)
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) handleListFlows(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	key := flowListCacheKey(t.ID)

	// Cache-aside: the full per-tenant list is read often (console refresh)
	// but only changes on a flow create/update/delete, each of which
	// invalidates this key. Serve the pre-encoded JSON verbatim on a hit.
	if b, ok := s.cache.Get(r.Context(), key); ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
		return
	}

	rows, err := s.db.Pool.Query(r.Context(), s.q.ListFlows, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	out := []Flow{}
	for rows.Next() {
		f, err := scanFlow(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, f)
	}

	b, err := json.Marshal(out)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encode failed")
		return
	}
	s.cache.Set(r.Context(), key, b, flowListCacheTTL)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (s *Server) handleGetFlow(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	row := s.db.Pool.QueryRow(r.Context(), s.q.GetFlow, id, t.ID)
	f, err := scanFlow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "flow not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) handlePatchFlow(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req patchFlowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var def interface{}
	if len(req.Definition) > 0 {
		def = req.Definition
	}
	row := s.db.Pool.QueryRow(r.Context(), s.q.UpdateFlow,
		req.Name, req.Description, req.IsActive, req.IsOutgoingEnd,
		def, req.WelcomeAudioURL, id, t.ID,
	)
	f, err := scanFlow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "flow not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	// Invalidate the orchestrator's cached definition (read on the call
	// path) and the tenant's cached list so the edit takes effect on the
	// next call/list instead of after the TTL. Best-effort.
	if err := s.cache.Delete(r.Context(), orchestrator.FlowCacheKey(id)); err != nil {
		log.Printf("flows: cache invalidate %s failed: %v", id, err)
	}
	s.invalidateFlowList(r.Context(), t.ID)
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) handleDeleteFlow(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	tag, err := s.db.Pool.Exec(r.Context(), s.q.DeleteFlow, id, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "flow not found")
		return
	}
	if err := s.cache.Delete(r.Context(), orchestrator.FlowCacheKey(id)); err != nil {
		log.Printf("flows: cache invalidate %s failed: %v", id, err)
	}
	s.invalidateFlowList(r.Context(), t.ID)
	w.WriteHeader(http.StatusNoContent)
}

// handleSimulateFlow runs the flow through the pure-Go simulator and
// returns a step trace. Nothing actually plays / dials / posts — see
// internal/orchestrator/simulator.go for the recorded-vs-executed split.
//
// Operator can pre-stage DTMF, caller ID, vars, and a fake clock so the
// trace is deterministic. Useful as a "Test flow" button in the editor.
func (s *Server) handleSimulateFlow(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var input orchestrator.SimInput
	// Empty body is fine — default caller, no DTMF, etc. Only fail on
	// a malformed body so a `POST /simulate` with no JSON Just Works.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}

	var defJSON []byte
	err = s.db.Pool.QueryRow(r.Context(), s.q.GetFlowDefinition,
		id, t.ID,
	).Scan(&defJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "flow not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	result, err := orchestrator.SimulateFlow(defJSON, input)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleSimulateStart begins an interactive session. The walker runs
// until it hits a Menu/Gather (then pauses for input) or the flow ends.
// Client receives a session_id to PATCH input into later.
func (s *Server) handleSimulateStart(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var input orchestrator.SimInput
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	var defJSON []byte
	err = s.db.Pool.QueryRow(r.Context(), s.q.GetFlowDefinition,
		id, t.ID,
	).Scan(&defJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "flow not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	snap, err := s.simManager.Start(defJSON, input)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// handleSimulateInput resumes a paused session with the supplied DTMF.
// 404 if the session was GC'd (idle past TTL) — the UI restarts in
// that case.
func (s *Server) handleSimulateInput(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sid")
	var body struct {
		DTMF string `json:"dtmf"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	snap, err := s.simManager.Input(sid, body.DTMF)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap)
}
