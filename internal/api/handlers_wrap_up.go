package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shridarpatil/tring/internal/callrow"
	"github.com/shridarpatil/tring/internal/webhook"
)

// Webhooks delivers registered webhooks; *webhook.Notifier implements it.
type Webhooks interface {
	Send(tenantID, webhookID uuid.UUID, eventType string, data interface{})
}

const (
	maxWrapUpNotes = 2000
	maxWrapUpTags  = 10
)

// wrapUpOptions is what the agent's wrap-up form offers: live labels only.
type wrapUpOptions struct {
	Dispositions []wrapUpOption `json:"dispositions"`
	Tags         []wrapUpOption `json:"tags"`
}

type wrapUpOption struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// handleWrapUpOptions is GET /v1/sdk/wrap-up-options: the active dispositions
// and tags of the agent's tenant (the admin lists need a full-scope key).
func (s *Server) handleWrapUpOptions(w http.ResponseWriter, r *http.Request) {
	claims := agentFromCtx(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no agent")
		return
	}
	out := wrapUpOptions{Dispositions: []wrapUpOption{}, Tags: []wrapUpOption{}}
	for _, l := range []struct {
		query string
		into  *[]wrapUpOption
	}{{s.q.ListDispositions, &out.Dispositions}, {s.q.ListCallTags, &out.Tags}} {
		rows, err := s.db.Pool.Query(r.Context(), l.query, claims.TenantID, false)
		if err != nil {
			s.writeServerError(w, r, http.StatusInternalServerError, "request failed", err)
			return
		}
		for rows.Next() {
			var o wrapUpOption
			var archived, created any
			if err := rows.Scan(&o.ID, &o.Name, &archived, &created); err != nil {
				rows.Close()
				s.writeServerError(w, r, http.StatusInternalServerError, "request failed", err)
				return
			}
			*l.into = append(*l.into, o)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			s.writeServerError(w, r, http.StatusInternalServerError, "request failed", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// wrapUpInput is a validated disposition + notes + tag set.
type wrapUpInput struct {
	disposition *uuid.UUID
	notes       *string
	tags        []uuid.UUID // de-duplicated
}

// checkWrapUpInput enforces the size limits; the id checks need the database
// (checkWrapUpIDs). It returns the message to answer 400 with, or "".
func checkWrapUpInput(in *wrapUpInput) string {
	if in.notes != nil && utf8.RuneCountInString(*in.notes) > maxWrapUpNotes {
		return "notes too long (max 2000 characters)"
	}
	seen := make(map[uuid.UUID]bool, len(in.tags))
	uniq := in.tags[:0:0]
	for _, id := range in.tags {
		if !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	in.tags = uniq
	if len(in.tags) > maxWrapUpTags {
		return "too many tags (max 10)"
	}
	return ""
}

// checkWrapUpIDs confirms the disposition and every tag are live rows of the
// tenant. call_tag_links has no tenant-composite FK, so this is the only thing
// keeping another tenant's tag off a call: it MUST run before any insert.
func (s *Server) checkWrapUpIDs(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in wrapUpInput) (bool, error) {
	if in.disposition != nil {
		var n int
		if err := tx.QueryRow(ctx, s.q.CountActiveDispositionsIn, tenantID, []uuid.UUID{*in.disposition}).Scan(&n); err != nil {
			return false, err
		}
		if n != 1 {
			return false, nil
		}
	}
	if len(in.tags) > 0 {
		var n int
		if err := tx.QueryRow(ctx, s.q.CountActiveTagsIn, tenantID, in.tags).Scan(&n); err != nil {
			return false, err
		}
		if n != len(in.tags) {
			return false, nil
		}
	}
	return true, nil
}

// replaceTagLinks sets the call's tag set to tags.
func (s *Server) replaceTagLinks(ctx context.Context, tx pgx.Tx, tenantID, callID uuid.UUID, tags []uuid.UUID) error {
	if _, err := tx.Exec(ctx, s.q.DeleteCallTagLinks, tenantID, callID); err != nil {
		return err
	}
	for _, id := range tags {
		if _, err := tx.Exec(ctx, s.q.InsertCallTagLink, tenantID, callID, id); err != nil {
			return err
		}
	}
	return nil
}

// emitWrapUp sends call.wrapped_up to the webhook the flow picked, when there
// is one. Called only after the wrap-up transaction has committed.
func (s *Server) emitWrapUp(ctx context.Context, tenantID, callID uuid.UUID, hookID *uuid.UUID) {
	if s.hooks == nil || hookID == nil || *hookID == uuid.Nil {
		return
	}
	c, err := callrow.Get(ctx, s.db, s.q, tenantID, callID)
	if err != nil {
		s.log.Warn("wrap-up webhook payload lookup failed", "call", callID, "err", err)
		return
	}
	s.hooks.Send(tenantID, *hookID, webhook.EventCallWrappedUp, callrow.Payload(c))
}

type saveWrapUpRequest struct {
	CallID      uuid.UUID   `json:"call_id"`
	Disposition *uuid.UUID  `json:"disposition_id"`
	Notes       *string     `json:"notes"`
	TagIDs      []uuid.UUID `json:"tag_ids"`
}

// handleSaveWrapUp is POST /v1/sdk/calls/wrap-up: the wrapping agent's one-shot
// save. The outcome is committed first; ending the agent's wrap-up on the
// gateway is best-effort (the timer ends it anyway), so a gateway failure never
// fails the save.
func (s *Server) handleSaveWrapUp(w http.ResponseWriter, r *http.Request) {
	claims := agentFromCtx(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no agent")
		return
	}
	agentID, err := uuid.Parse(claims.Subject)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "bad agent id")
		return
	}
	var req saveWrapUpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CallID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "call_id required")
		return
	}
	in := wrapUpInput{disposition: req.Disposition, notes: req.Notes, tags: req.TagIDs}
	if msg := checkWrapUpInput(&in); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	ctx, tenantID := r.Context(), claims.TenantID

	var owner *uuid.UUID
	var require, wrapped, open bool
	err = s.db.Pool.QueryRow(ctx, s.q.GetCallWrapUpState, tenantID, req.CallID).Scan(&owner, &require, &wrapped, &open)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (owner == nil || *owner != agentID)) {
		writeError(w, http.StatusNotFound, "call not found")
		return
	}
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "request failed", err)
		return
	}
	if wrapped || !open {
		writeError(w, http.StatusConflict, "wrap-up already closed")
		return
	}
	if require && in.disposition == nil {
		writeError(w, http.StatusBadRequest, "disposition required")
		return
	}

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "request failed", err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	ok, err := s.checkWrapUpIDs(ctx, tx, tenantID, in)
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "request failed", err)
		return
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown or archived disposition or tag")
		return
	}
	var hookID *uuid.UUID
	err = tx.QueryRow(ctx, s.q.SaveCallWrapUp, tenantID, req.CallID, in.disposition, in.notes, agentID).Scan(&hookID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Closed (timer, another save) between the state read and the write.
		writeError(w, http.StatusConflict, "wrap-up already closed")
		return
	}
	if err == nil {
		err = s.replaceTagLinks(ctx, tx, tenantID, req.CallID, in.tags)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "request failed", err)
		return
	}

	s.emitWrapUp(ctx, tenantID, req.CallID, hookID)
	if err := s.gatewayControl(ctx, tenantID, "/control/wrap-up-done", map[string]string{
		"sip": claims.SipUsername, "call_id": req.CallID.String(),
	}); err != nil {
		// 404 = the timer or another path already ended it; anything else is a
		// gateway problem. The outcome is saved either way.
		s.log.Warn("wrap-up-done not delivered", "call", req.CallID, "err", err)
	}
	c, err := callrow.Get(ctx, s.db, s.q, tenantID, req.CallID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// handlePatchWrapUp is PATCH /v1/calls/{id}/wrap-up: a CRM or admin edits a
// call's outcome at any time, including after the wrap-up window closed. Fields
// present replace, absent fields stay; tag_ids replaces the whole set. It never
// touches the wrap-up state, so a timed-out call stays "timeout".
func (s *Server) handlePatchWrapUp(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	callID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	// RawMessage tells an absent field from an explicit null (which clears).
	var req struct {
		Disposition json.RawMessage `json:"disposition_id"`
		Notes       json.RawMessage `json:"notes"`
		TagIDs      json.RawMessage `json:"tag_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	ctx := r.Context()
	// Only fields the request sets are written (the query's set flags); nothing
	// is read first, so a save that commits meanwhile is never overwritten.
	var in wrapUpInput
	setDisp, setNotes, setTags := req.Disposition != nil, req.Notes != nil, req.TagIDs != nil
	if setDisp {
		if err := json.Unmarshal(req.Disposition, &in.disposition); err != nil {
			writeError(w, http.StatusBadRequest, "invalid disposition_id")
			return
		}
	}
	if setNotes {
		if err := json.Unmarshal(req.Notes, &in.notes); err != nil {
			writeError(w, http.StatusBadRequest, "invalid notes")
			return
		}
	}
	if setTags {
		if err := json.Unmarshal(req.TagIDs, &in.tags); err != nil {
			writeError(w, http.StatusBadRequest, "invalid tag_ids")
			return
		}
	}
	if msg := checkWrapUpInput(&in); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "request failed", err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	ok, err := s.checkWrapUpIDs(ctx, tx, t.ID, in)
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "request failed", err)
		return
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown or archived disposition or tag")
		return
	}
	var hookID *uuid.UUID
	err = tx.QueryRow(ctx, s.q.PatchCallWrapUp, t.ID, callID, in.disposition, in.notes, setDisp, setNotes).Scan(&hookID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "call not found")
		return
	}
	if err == nil && setTags {
		err = s.replaceTagLinks(ctx, tx, t.ID, callID, in.tags)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "request failed", err)
		return
	}
	s.emitWrapUp(ctx, t.ID, callID, hookID)
	c, err := callrow.Get(ctx, s.db, s.q, t.ID, callID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "call not found")
		return
	}
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "request failed", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}
