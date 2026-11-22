package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Dispositions and call tags are the two tenant-managed label lists an agent
// picks from at wrap-up. They share one shape, so one set of handlers is
// parameterized by the query strings. Each is shared (no teams: every team's
// wrap-up offers it) or limited to chosen teams.

const maxLabelLen = 64

// labelItem is the JSON shape of a disposition or call tag.
type labelItem struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Archived  bool      `json:"archived"`
	CreatedAt time.Time `json:"created_at"`
	Teams     []string  `json:"teams"` // [] = shared by all teams
}

// labelQueries names the SQL for one label resource.
type labelQueries struct{ create, list, update, deleteTeams, insertTeam string }

func (s *Server) dispositionQueries() labelQueries {
	return labelQueries{s.q.CreateDisposition, s.q.ListDispositions, s.q.UpdateDisposition,
		s.q.DeleteDispositionTeams, s.q.InsertDispositionTeam}
}

func (s *Server) callTagQueries() labelQueries {
	return labelQueries{s.q.CreateCallTag, s.q.ListCallTags, s.q.UpdateCallTag,
		s.q.DeleteCallTagTeams, s.q.InsertCallTagTeam}
}

func (s *Server) handleListDispositions(w http.ResponseWriter, r *http.Request) {
	s.listLabels(w, r, s.dispositionQueries())
}
func (s *Server) handleCreateDisposition(w http.ResponseWriter, r *http.Request) {
	s.createLabel(w, r, s.dispositionQueries())
}
func (s *Server) handleUpdateDisposition(w http.ResponseWriter, r *http.Request) {
	s.updateLabel(w, r, s.dispositionQueries())
}
func (s *Server) handleListCallTags(w http.ResponseWriter, r *http.Request) {
	s.listLabels(w, r, s.callTagQueries())
}
func (s *Server) handleCreateCallTag(w http.ResponseWriter, r *http.Request) {
	s.createLabel(w, r, s.callTagQueries())
}
func (s *Server) handleUpdateCallTag(w http.ResponseWriter, r *http.Request) {
	s.updateLabel(w, r, s.callTagQueries())
}

func scanLabel(row pgx.Row) (labelItem, error) {
	var it labelItem
	var archivedAt *time.Time
	err := row.Scan(&it.ID, &it.Name, &archivedAt, &it.CreatedAt, &it.Teams)
	it.Archived = archivedAt != nil
	if it.Teams == nil {
		it.Teams = []string{}
	}
	return it, err
}

// cleanTeams trims, de-duplicates and sorts a label's team names, dropping
// blank ones (so [" "] means shared, like []).
func cleanTeams(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, n := range in {
		n = strings.TrimSpace(n)
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// setLabelTeams replaces label id's team set inside tx. When a name isn't one
// of the tenant's teams it writes nothing and returns that name with ok false.
func (s *Server) setLabelTeams(ctx context.Context, tx pgx.Tx, q labelQueries, tenantID, id uuid.UUID, teams []string) (unknown string, ok bool, err error) {
	err = tx.QueryRow(ctx, s.q.FirstUnknownTeam, tenantID, teams).Scan(&unknown)
	if err == nil {
		return unknown, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, q.deleteTeams, tenantID, id); err != nil {
		return "", false, err
	}
	for _, t := range teams {
		if _, err := tx.Exec(ctx, q.insertTeam, tenantID, id, t); err != nil {
			return "", false, err
		}
	}
	return "", true, nil
}

// cleanLabelName trims and validates a name; ok is false when it is empty or
// too long.
func cleanLabelName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	return name, name != "" && utf8.RuneCountInString(name) <= maxLabelLen
}

func (s *Server) listLabels(w http.ResponseWriter, r *http.Request, q labelQueries) {
	t := tenantFromCtx(r.Context())
	incl := r.URL.Query().Get("include_archived") == "1"
	rows, err := s.db.Pool.Query(r.Context(), q.list, t.ID, incl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "request failed")
		return
	}
	defer rows.Close()
	items := []labelItem{}
	for rows.Next() {
		it, err := scanLabel(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "request failed")
			return
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "request failed")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createLabel(w http.ResponseWriter, r *http.Request, q labelQueries) {
	t := tenantFromCtx(r.Context())
	var req struct {
		Name  string   `json:"name"`
		Teams []string `json:"teams"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	name, ok := cleanLabelName(req.Name)
	if !ok {
		writeError(w, http.StatusBadRequest, "name required, max 64 characters")
		return
	}
	ctx := r.Context()
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "request failed")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	it, err := scanLabel(tx.QueryRow(ctx, q.create, t.ID, name))
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "request failed")
		return
	}
	if len(req.Teams) > 0 {
		it.Teams = cleanTeams(req.Teams)
		if !s.writeLabelTeams(w, r, tx, q, t.ID, it.ID, it.Teams) {
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "request failed")
		return
	}
	writeJSON(w, http.StatusCreated, it)
}

func (s *Server) updateLabel(w http.ResponseWriter, r *http.Request, q labelQueries) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var req struct {
		Name     *string   `json:"name"`
		Archived *bool     `json:"archived"`
		Teams    *[]string `json:"teams"` // present replaces the set; [] = shared
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Name != nil {
		name, ok := cleanLabelName(*req.Name)
		if !ok {
			writeError(w, http.StatusBadRequest, "name required, max 64 characters")
			return
		}
		req.Name = &name
	}
	ctx := r.Context()
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "request failed")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	it, err := scanLabel(tx.QueryRow(ctx, q.update, t.ID, id, req.Name, req.Archived))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "request failed")
		return
	}
	if req.Teams != nil {
		it.Teams = cleanTeams(*req.Teams)
		if !s.writeLabelTeams(w, r, tx, q, t.ID, id, it.Teams) {
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "request failed")
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// writeLabelTeams runs setLabelTeams and answers the request on failure (400
// for an unknown team); ok is false when it did.
func (s *Server) writeLabelTeams(w http.ResponseWriter, r *http.Request, tx pgx.Tx, q labelQueries, tenantID, id uuid.UUID, teams []string) bool {
	unknown, ok, err := s.setLabelTeams(r.Context(), tx, q, tenantID, id, teams)
	switch {
	case err != nil && isForeignKeyViolation(err):
		// The team was deleted between the check and the insert.
		writeError(w, http.StatusBadRequest, "unknown team")
		return false
	case err != nil:
		s.writeServerError(w, r, http.StatusInternalServerError, "request failed", err)
		return false
	case !ok:
		writeError(w, http.StatusBadRequest, "unknown team: "+unknown)
		return false
	}
	return true
}
