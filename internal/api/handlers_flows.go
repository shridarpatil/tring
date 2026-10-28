package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shridarpatil/tring/internal/brain"
	"github.com/shridarpatil/tring/internal/flowstore"
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
		s.log.Warn("flow list cache invalidate failed", "tenant", tenantID, "err", err)
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

// ttsSubdir is the audio-library subdir holding prompt audio synthesized from
// node text. A node audio_file under this subdir is TTS-managed (replaced when
// the text changes); an uploaded file is not, so it always wins.
const ttsSubdir = "tts"

// prerenderFlowAudio synthesizes spoken audio for IVR nodes that have prompt
// text (greeting_text) but no uploaded audio, then stamps the node's audio_file
// with the generated file — so both media planes play it through the normal
// audio_file path with no call-time TTS. It mutates a generic-map round-trip of
// the definition (preserving every field) and returns the updated JSON; on any
// problem it returns the definition unchanged (best-effort, never blocks a
// save). Text with unresolved {{vars}} is skipped — those can only be rendered
// at call time, which we don't do.
func (s *Server) prerenderFlowAudio(ctx context.Context, def json.RawMessage) json.RawMessage {
	if s.tts == nil || !s.tts.Enabled() || len(def) == 0 {
		return def
	}
	var root map[string]any
	if err := json.Unmarshal(def, &root); err != nil {
		return def // not our job to validate here; store as-is
	}
	nodes, ok := root["nodes"].([]any)
	if !ok {
		return def
	}
	changed := false
	for _, nraw := range nodes {
		node, ok := nraw.(map[string]any)
		if !ok {
			continue
		}
		cfg, ok := node["config"].(map[string]any)
		if !ok {
			continue
		}
		text := strings.TrimSpace(stringOf(cfg["greeting_text"]))
		af := stringOf(cfg["audio_file"])
		managed := strings.Contains(af, "/"+ttsSubdir+"/")
		switch {
		case text != "" && !strings.Contains(text, "{{") && (af == "" || managed):
			fp, err := s.synthFlowText(ctx, text)
			if err != nil {
				s.log.Warn("tts: flow prompt synth failed; node stays silent", "err", err)
				continue
			}
			if af != fp {
				cfg["audio_file"] = fp
				changed = true
			}
		case text == "" && managed:
			// Text cleared on a node we'd synthesized for — drop the stale audio.
			delete(cfg, "audio_file")
			changed = true
		}
	}
	if !changed {
		return def
	}
	if out, err := json.Marshal(root); err == nil {
		return out
	}
	return def
}

// synthFlowText renders text to a cached audio file and returns its FS-visible
// playback path (the value stored in a node's audio_file).
func (s *Server) synthFlowText(ctx context.Context, text string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	diskPath, err := s.tts.Synthesize(cctx, text, "")
	if err != nil {
		return "", err
	}
	return s.audioDir + "/" + ttsSubdir + "/" + filepath.Base(diskPath), nil
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
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
	// can omit it without sending an empty object. Node prompt text is
	// pre-rendered to audio here so both planes play it as a normal file.
	var def interface{}
	if len(req.Definition) > 0 {
		def = s.prerenderFlowAudio(r.Context(), req.Definition)
	}

	row := s.db.Pool.QueryRow(r.Context(), s.q.CreateFlow,
		t.ID, req.Name, req.Description,
		req.IsActive, req.IsOutgoingEnd,
		def, req.WelcomeAudioURL,
	)
	f, err := scanFlow(row)
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "insert failed", err)
		return
	}
	s.invalidateFlowList(r.Context(), t.ID)
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) handleListFlows(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())

	limit, offset, paged := pageParams(r)
	if paged {
		s.listFlowsPage(w, r, t.ID, limit, offset)
		return
	}

	// Unpaged: the full per-tenant list, served as a uniform envelope and
	// cache-aside. The list is read often (console refresh, flow dropdowns)
	// but only changes on a create/update/delete, each of which invalidates
	// this key. Serve the pre-encoded JSON verbatim on a hit.
	key := flowListCacheKey(t.ID)
	if b, ok := s.cache.Get(r.Context(), key); ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
		return
	}

	out, err := s.scanFlows(r, s.q.ListFlows, t.ID)
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "query failed", err)
		return
	}

	b, err := json.Marshal(pageEnvelope{Items: out, Total: len(out), Limit: len(out), Offset: 0})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encode failed")
		return
	}
	s.cache.Set(r.Context(), key, b, flowListCacheTTL)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// listFlowsPage serves one numbered page of flows (cache-bypassed — paging is a
// console-only access pattern, and the full-list cache stays the hot path).
func (s *Server) listFlowsPage(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID, limit, offset int) {
	var total int
	if err := s.db.Pool.QueryRow(r.Context(), s.q.CountFlows, tenantID).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "count failed")
		return
	}
	out, err := s.scanFlows(r, s.q.ListFlowsPage, tenantID, limit, offset)
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "query failed", err)
		return
	}
	writeJSON(w, http.StatusOK, pageEnvelope{Items: out, Total: total, Limit: limit, Offset: offset})
}

// scanFlows runs a flow-listing query and scans every row into a non-nil slice.
func (s *Server) scanFlows(r *http.Request, query string, args ...interface{}) ([]Flow, error) {
	rows, err := s.db.Pool.Query(r.Context(), query, args...)
	if err != nil {
		return nil, errors.New("query failed")
	}
	defer rows.Close()
	out := []Flow{}
	for rows.Next() {
		f, err := scanFlow(rows)
		if err != nil {
			return nil, errors.New("scan failed")
		}
		out = append(out, f)
	}
	return out, nil
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
		def = s.prerenderFlowAudio(r.Context(), req.Definition)
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
		s.writeServerError(w, r, http.StatusInternalServerError, "update failed", err)
		return
	}
	// Invalidate the gateway's cached definition (read on the call
	// path) and the tenant's cached list so the edit takes effect on the
	// next call/list instead of after the TTL. Best-effort.
	if err := s.cache.Delete(r.Context(), flowstore.FlowCacheKey(id)); err != nil {
		s.log.Warn("flow definition cache invalidate failed", "flow", id, "err", err)
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
	// The numbers routed here, read before the delete nulls their flow_id: their
	// cached DID routes still name this flow and must go with it.
	dids, err := s.flowInboundNumbers(r.Context(), id, t.ID)
	if err != nil {
		s.log.Warn("list flow numbers failed; their cached routes expire by TTL", "flow", id, "err", err)
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
	if err := s.cache.Delete(r.Context(), flowstore.FlowCacheKey(id)); err != nil {
		s.log.Warn("flow definition cache invalidate failed", "flow", id, "err", err)
	}
	s.invalidateDIDs(r.Context(), dids...)
	s.invalidateFlowList(r.Context(), t.ID)
	w.WriteHeader(http.StatusNoContent)
}

// flowInboundNumbers lists the inbound DIDs routed to a flow.
func (s *Server) flowInboundNumbers(ctx context.Context, flowID, tenantID uuid.UUID) ([]string, error) {
	rows, err := s.db.Pool.Query(ctx, s.q.ListFlowInboundNumbers, flowID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var dids []string
	for rows.Next() {
		var e164 string
		if err := rows.Scan(&e164); err != nil {
			return nil, err
		}
		dids = append(dids, e164)
	}
	return dids, rows.Err()
}

// handleSimulateFlow runs the flow through the pure-Go simulator and
// returns a step trace. Nothing actually plays / dials / posts — see
// internal/brain/simulator.go for the recorded-vs-executed split.
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

	var input brain.SimInput
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

	result, err := brain.SimulateFlow(defJSON, input)
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
	var input brain.SimInput
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
	snap, err := s.simManager.Start(t.ID, defJSON, input)
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
	t := tenantFromCtx(r.Context())
	sid := chi.URLParam(r, "sid")
	var body struct {
		DTMF string `json:"dtmf"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	snap, err := s.simManager.Input(t.ID, sid, body.DTMF)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap)
}
