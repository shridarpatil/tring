package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Dispositions and call tags are the two tenant-managed label lists an agent
// picks from at wrap-up. They share one shape, so one set of handlers is
// parameterized by the query strings.

const maxLabelLen = 64

// labelItem is the JSON shape of a disposition or call tag.
type labelItem struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Archived  bool      `json:"archived"`
	CreatedAt time.Time `json:"created_at"`
}

// labelQueries names the SQL for one label resource.
type labelQueries struct{ create, list, update string }

func (s *Server) dispositionQueries() labelQueries {
	return labelQueries{s.q.CreateDisposition, s.q.ListDispositions, s.q.UpdateDisposition}
}

func (s *Server) callTagQueries() labelQueries {
	return labelQueries{s.q.CreateCallTag, s.q.ListCallTags, s.q.UpdateCallTag}
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
	err := row.Scan(&it.ID, &it.Name, &archivedAt, &it.CreatedAt)
	it.Archived = archivedAt != nil
	return it, err
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
		Name string `json:"name"`
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
	it, err := scanLabel(s.db.Pool.QueryRow(r.Context(), q.create, t.ID, name))
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "name already exists")
			return
		}
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
		Name     *string `json:"name"`
		Archived *bool   `json:"archived"`
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
	it, err := scanLabel(s.db.Pool.QueryRow(r.Context(), q.update, t.ID, id, req.Name, req.Archived))
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
	writeJSON(w, http.StatusOK, it)
}
