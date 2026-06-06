package api

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ---- Audio library ------------------------------------------------------
//
// A tenant uploads WAV/MP3 once and reuses a file anywhere a sound plays —
// IVR greeting/menu/gather/hangup prompts and queue hold music. The API writes
// the bytes into a shared dir FreeSWITCH bind-mounts (s.audioDir); fs_path is
// the FS-visible path stored on the asset and used as the value in a flow
// node's audio_file / tenants.hold_music. Mirrors the dynamic-gateway pattern
// (internal/freeswitch/dynamic_gateway.go).

// maxAudioBytes caps an upload so a huge file can't exhaust disk/memory.
const maxAudioBytes = 10 << 20 // 10 MiB

// audioFormats maps an accepted extension to the stored format tag. WAV plays
// natively; MP3 needs mod_shout (enabled in modules.conf.xml).
var audioFormats = map[string]string{".wav": "wav", ".mp3": "mp3"}

type Audio struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	Name      string    `json:"name"`
	FSPath    string    `json:"fs_path"`
	Format    string    `json:"format"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

// diskPathFor maps an asset's FS-visible fs_path back to where the API can read
// it on its own filesystem. They're equal in the standard docker deploy; for
// host-run dev (audio_dir != audio_fs_dir) it swaps the prefix.
func (s *Server) diskPathFor(fsPath string) string {
	rel := strings.TrimPrefix(fsPath, s.audioFSDir)
	return filepath.Join(s.audioDir, rel)
}

func (s *Server) handleUploadAudio(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	if s.audioDir == "" {
		writeError(w, http.StatusServiceUnavailable, "audio uploads are not configured")
		return
	}
	// Cap the whole request so a giant upload can't exhaust memory/disk; the
	// envelope allowance covers the multipart framing around the file itself.
	r.Body = http.MaxBytesReader(w, r.Body, maxAudioBytes+1<<20)
	if err := r.ParseMultipartForm(maxAudioBytes); err != nil {
		writeError(w, http.StatusBadRequest, "invalid upload or file too large (max 10MB)")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	format, ok := audioFormats[ext]
	if !ok {
		writeError(w, http.StatusBadRequest, "only .wav and .mp3 files are supported")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = strings.TrimSuffix(hdr.Filename, ext)
	}
	if name == "" {
		name = "audio"
	}

	// uuid filename → no path traversal, no collisions. fs_path uses the
	// FS-visible prefix; the bytes go to the matching spot under audioDir.
	fileID := uuid.New()
	tenantDir := filepath.Join(s.audioDir, t.ID.String())
	if err := os.MkdirAll(tenantDir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	diskPath := filepath.Join(tenantDir, fileID.String()+ext)
	dst, err := os.Create(diskPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "write failed")
		return
	}
	written, copyErr := io.Copy(dst, io.LimitReader(file, maxAudioBytes+1))
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil || written > maxAudioBytes {
		_ = os.Remove(diskPath)
		writeError(w, http.StatusBadRequest, "upload failed or exceeds 10MB")
		return
	}
	fsPath := s.audioFSDir + "/" + t.ID.String() + "/" + fileID.String() + ext

	var a Audio
	err = s.db.Pool.QueryRow(r.Context(), s.q.CreateAudio,
		t.ID, name, fsPath, format, written).Scan(
		&a.ID, &a.TenantID, &a.Name, &a.FSPath, &a.Format, &a.SizeBytes, &a.CreatedAt)
	if isUniqueViolation(err) {
		_ = os.Remove(diskPath)
		writeError(w, http.StatusConflict, "a sound with this name already exists")
		return
	}
	if err != nil {
		_ = os.Remove(diskPath)
		writeError(w, http.StatusInternalServerError, "save failed")
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) handleListAudio(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	rows, err := s.db.Pool.Query(r.Context(), s.q.ListAudio, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	out := []Audio{}
	for rows.Next() {
		var a Audio
		if err := rows.Scan(&a.ID, &a.TenantID, &a.Name, &a.FSPath, &a.Format, &a.SizeBytes, &a.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetAudioFile streams the raw bytes back so the console can preview/play
// the sound. http.ServeFile sets the content type and supports range requests.
func (s *Server) handleGetAudioFile(w http.ResponseWriter, r *http.Request) {
	a, ok := s.loadAudio(w, r)
	if !ok {
		return
	}
	http.ServeFile(w, r, s.diskPathFor(a.FSPath))
}

func (s *Server) handleDeleteAudio(w http.ResponseWriter, r *http.Request) {
	a, ok := s.loadAudio(w, r)
	if !ok {
		return
	}
	if _, err := s.db.Pool.Exec(r.Context(), s.q.DeleteAudio, a.ID, a.TenantID); err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	_ = os.Remove(s.diskPathFor(a.FSPath)) // best-effort; the row is the source of truth
	w.WriteHeader(http.StatusNoContent)
}

// loadAudio fetches the tenant's asset named in the {id} path param, writing a
// 400/404 and returning ok=false on a bad id / miss.
func (s *Server) loadAudio(w http.ResponseWriter, r *http.Request) (Audio, bool) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return Audio{}, false
	}
	var a Audio
	err = s.db.Pool.QueryRow(r.Context(), s.q.GetAudio, id, t.ID).Scan(
		&a.ID, &a.TenantID, &a.Name, &a.FSPath, &a.Format, &a.SizeBytes, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "sound not found")
		return Audio{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return Audio{}, false
	}
	return a, true
}
