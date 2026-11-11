package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shridarpatil/tring/internal/campaign"
	"github.com/shridarpatil/tring/internal/dial"
	"github.com/shridarpatil/tring/internal/ivr"
	"github.com/shridarpatil/tring/internal/media/prompt"
)

func (s *Server) campaignStore() *campaign.Store {
	return &campaign.Store{Pool: s.db.Pool, Q: s.q, Limits: s.campaignLimits}
}
func (s *Server) campaignID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, 400, "invalid campaign id")
		return id, false
	}
	return id, true
}
func (s *Server) campaignError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, 404, "campaign not found")
	case errors.Is(err, campaign.ErrConflict):
		writeError(w, 409, err.Error())
	default:
		s.log.Warn("campaign request failed", "err", err)
		writeError(w, 400, err.Error())
	}
}
func campaignDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeError(w, 400, "invalid request body")
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		writeError(w, 400, "expected one JSON object")
		return false
	}
	return true
}

type campaignWrite struct {
	Name     string          `json:"name"`
	Config   campaign.Config `json:"config"`
	Revision int             `json:"revision"`
}

func (s *Server) handleCreateCampaign(w http.ResponseWriter, r *http.Request) {
	var req campaignWrite
	if !campaignDecode(w, r, &req) {
		return
	}
	id, err := s.campaignStore().Create(r.Context(), tenantFromCtx(r.Context()).ID, req.Name, req.Config)
	if err != nil {
		s.campaignError(w, err)
		return
	}
	c, err := s.campaignStore().Get(r.Context(), tenantFromCtx(r.Context()).ID, id)
	if err != nil {
		s.campaignError(w, err)
		return
	}
	writeJSON(w, 201, c)
}
func (s *Server) handleUpdateCampaign(w http.ResponseWriter, r *http.Request) {
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	var req campaignWrite
	if !campaignDecode(w, r, &req) {
		return
	}
	if err := s.campaignStore().Update(r.Context(), tenantFromCtx(r.Context()).ID, id, req.Name, req.Config, req.Revision); err != nil {
		s.campaignError(w, err)
		return
	}
	s.handleGetCampaign(w, r)
}
func (s *Server) handleGetCampaign(w http.ResponseWriter, r *http.Request) {
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	c, err := s.campaignStore().Get(r.Context(), tenantFromCtx(r.Context()).ID, id)
	if err != nil {
		s.campaignError(w, err)
		return
	}
	writeJSON(w, 200, c)
}
func (s *Server) handleListCampaigns(w http.ResponseWriter, r *http.Request) {
	limit, offset, _ := pageParams(r)
	items, total, err := s.campaignStore().List(r.Context(), tenantFromCtx(r.Context()).ID, limit, offset)
	if err != nil {
		s.campaignError(w, err)
		return
	}
	writeJSON(w, 200, pageEnvelope{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (s *Server) campaignCaller(ctx context.Context, tenant uuid.UUID, c campaign.Config) error {
	if err := s.campaignTeam(ctx, tenant, c); err != nil {
		return err
	}
	if c.WebhookID != "" {
		var valid bool
		if err := s.db.Pool.QueryRow(ctx, s.q.CampaignWebhookOwned, tenant, c.WebhookID).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return errors.New("select an active webhook belonging to this tenant")
		}
	}
	var owned bool
	if err := s.db.Pool.QueryRow(ctx, s.q.TenantOwnsNumber, tenant, c.CallerID).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return errors.New("caller ID must be an active number owned by this tenant")
	}
	rows, err := s.db.Pool.Query(ctx, s.q.SelectOutboundTrunks, tenant)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return errors.New("configure an outbound trunk before launching")
	}
	return nil
}

func (s *Server) handleCampaignAction(w http.ResponseWriter, r *http.Request) {
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	tenant := tenantFromCtx(r.Context()).ID
	var req struct {
		Revision    int        `json:"revision"`
		ScheduledAt *time.Time `json:"scheduled_at"`
	}
	if !campaignDecode(w, r, &req) {
		return
	}
	action := chi.URLParam(r, "action")
	if action == "start" || action == "schedule" || action == "resume" {
		c, err := s.campaignStore().Get(r.Context(), tenant, id)
		if err != nil {
			s.campaignError(w, err)
			return
		}
		if err = s.campaignCaller(r.Context(), tenant, c.Config); err != nil {
			s.campaignError(w, err)
			return
		}
		if err = s.checkCampaignPrepared(c.Prepared, c.Config.MaxSeconds); err != nil {
			s.campaignError(w, err)
			return
		}
	}
	if err := s.campaignStore().Transition(r.Context(), tenant, id, action, req.ScheduledAt, req.Revision); err != nil {
		s.campaignError(w, err)
		return
	}
	s.handleGetCampaign(w, r)
}

func (s *Server) handlePrepareCampaign(w http.ResponseWriter, r *http.Request) {
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	tenant := tenantFromCtx(r.Context()).ID
	var req struct {
		Revision int `json:"revision"`
	}
	if !campaignDecode(w, r, &req) {
		return
	}
	c, err := s.campaignStore().Get(r.Context(), tenant, id)
	if err != nil {
		s.campaignError(w, err)
		return
	}
	if c.Status != "draft" || c.Revision != req.Revision {
		s.campaignError(w, campaign.ErrConflict)
		return
	}
	prepared, err := s.prepareCampaign(r.Context(), tenant, c.Config)
	if err != nil {
		s.campaignError(w, err)
		return
	}
	if err = s.campaignStore().Prepare(r.Context(), tenant, id, prepared, c.Revision); err != nil {
		s.campaignError(w, err)
		return
	}
	s.handleGetCampaign(w, r)
}

func (s *Server) handleTestCampaign(w http.ResponseWriter, r *http.Request) {
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	tenant := tenantFromCtx(r.Context()).ID
	var req struct {
		To       string `json:"to"`
		Revision int    `json:"revision"`
	}
	if !campaignDecode(w, r, &req) {
		return
	}
	phone, err := dial.India(req.To)
	if err != nil {
		writeError(w, 400, "invalid test number")
		return
	}
	c, err := s.campaignStore().Get(r.Context(), tenant, id)
	if err != nil {
		s.campaignError(w, err)
		return
	}
	if err = s.campaignCaller(r.Context(), tenant, c.Config); err != nil {
		s.campaignError(w, err)
		return
	}
	if err = s.checkCampaignPrepared(c.Prepared, c.Config.MaxSeconds); err != nil {
		s.campaignError(w, err)
		return
	}
	attempt, err := s.campaignStore().Test(r.Context(), tenant, id, phone, req.Revision, s.campaignLimits)
	if err != nil {
		s.campaignError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"attempt_id": attempt})
}

// StartCampaignWorker is called once by the API role and returns when ctx ends.
func (s *Server) StartCampaignWorker(ctx context.Context) {
	w := campaign.Worker{Store: s.campaignStore(), Limits: s.campaignLimits, Log: s.log, Dispatch: func(ctx context.Context, tenant, id uuid.UUID) error {
		return s.gatewayControl(ctx, tenant, "/control/campaign", map[string]any{"attempt_id": id})
	}}
	w.Run(ctx)
}

func (s *Server) handleCampaignRecipients(w http.ResponseWriter, r *http.Request) {
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	tenant := tenantFromCtx(r.Context()).ID
	if _, err := s.campaignStore().Get(r.Context(), tenant, id); err != nil {
		s.campaignError(w, err)
		return
	}
	limit, offset, _ := pageParams(r)
	items, total, err := s.campaignStore().Recipients(r.Context(), tenant, id, limit, offset, r.URL.Query().Get("status"))
	if err != nil {
		s.campaignError(w, err)
		return
	}
	writeJSON(w, 200, pageEnvelope{Items: items, Total: total, Limit: limit, Offset: offset})
}
func (s *Server) handleCampaignAttempts(w http.ResponseWriter, r *http.Request) {
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	tenant := tenantFromCtx(r.Context()).ID
	if _, err := s.campaignStore().Get(r.Context(), tenant, id); err != nil {
		s.campaignError(w, err)
		return
	}
	limit, offset, _ := pageParams(r)
	items, total, err := s.campaignStore().Attempts(r.Context(), tenant, id, limit, offset)
	if err != nil {
		s.campaignError(w, err)
		return
	}
	writeJSON(w, 200, pageEnvelope{Items: items, Total: total, Limit: limit, Offset: offset})
}

type campaignImportRow struct {
	Phone       string `json:"phone"`
	ExternalRef string `json:"external_ref"`
}

func (s *Server) handleImportCampaign(w http.ResponseWriter, r *http.Request) {
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	var input []campaignImportRow
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			writeError(w, 400, "CSV must be at most 2 MB")
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, _, err := r.FormFile("file")
		if err != nil {
			writeError(w, 400, "file is required")
			return
		}
		defer file.Close()
		reader := csv.NewReader(file)
		reader.TrimLeadingSpace = true
		header, err := reader.Read()
		if err != nil {
			writeError(w, 400, "CSV needs a phone column")
			return
		}
		phoneCol, refCol := -1, -1
		for i, h := range header {
			switch strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")) {
			case "phone":
				phoneCol = i
			case "external_ref":
				refCol = i
			}
		}
		if phoneCol < 0 {
			writeError(w, 400, "CSV needs a phone column")
			return
		}
		for {
			row, err := reader.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				writeError(w, 400, "invalid CSV: "+err.Error())
				return
			}
			v := campaignImportRow{Phone: row[phoneCol]}
			if refCol >= 0 {
				v.ExternalRef = row[refCol]
			}
			input = append(input, v)
			if len(input) > 10000 {
				writeError(w, 400, "maximum 10000 recipients per import")
				return
			}
		}
	} else {
		var req struct {
			Recipients []campaignImportRow `json:"recipients"`
		}
		if !campaignDecode(w, r, &req) {
			return
		}
		input = req.Recipients
	}
	if len(input) == 0 || len(input) > 10000 {
		writeError(w, 400, "provide 1–10000 recipients")
		return
	}
	valid := []campaign.Recipient{}
	rejected := []map[string]any{}
	seen := map[string]bool{}
	duplicates := 0
	for i, row := range input {
		phone, err := dial.India(row.Phone)
		if err != nil || len(row.ExternalRef) > 200 {
			rejected = append(rejected, map[string]any{"row": i + 1, "phone": row.Phone, "error": "invalid number or reference exceeds 200 bytes"})
			continue
		}
		if seen[phone] {
			duplicates++
			continue
		}
		seen[phone] = true
		valid = append(valid, campaign.Recipient{Phone: phone, ExternalRef: row.ExternalRef})
	}
	added, err := s.campaignStore().Import(r.Context(), tenantFromCtx(r.Context()).ID, id, valid)
	if err != nil {
		s.campaignError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"added": added, "duplicates": duplicates + len(valid) - added, "rejected": rejected})
}

func csvSafe(v string) string {
	if v != "" && strings.ContainsAny(v[:1], "=+-@\t\r\n") {
		return "'" + v
	}
	return v
}
func (s *Server) handleExportCampaign(w http.ResponseWriter, r *http.Request) {
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	tenant := tenantFromCtx(r.Context()).ID
	if _, err := s.campaignStore().Get(r.Context(), tenant, id); err != nil {
		s.campaignError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="campaign-`+id.String()+`.csv"`)
	writer := csv.NewWriter(w)
	defer writer.Flush()
	_ = writer.Write([]string{"recipient_id", "phone", "external_ref", "status", "attempts"})
	for offset := 0; ; offset += 1000 {
		rows, total, err := s.campaignStore().Recipients(r.Context(), tenant, id, 1000, offset, "")
		if err != nil {
			s.log.Warn("campaign export failed", "err", err)
			return
		}
		for _, row := range rows {
			if err = writer.Write([]string{row.ID.String(), csvSafe(row.Phone), csvSafe(row.ExternalRef), row.Status, fmt.Sprint(row.Attempts)}); err != nil {
				return
			}
		}
		if offset+len(rows) >= total {
			return
		}
	}
}

func (s *Server) prepareCampaign(ctx context.Context, tenant uuid.UUID, c campaign.Config) (campaign.Prepared, error) {
	var p campaign.Prepared
	if err := c.Validate(); err != nil {
		return p, err
	}
	switch c.Type {
	case "agent":
		if err := s.campaignTeam(ctx, tenant, c); err != nil {
			return p, err
		}
		p.Team = c.Team
	case "audio":
		var a Audio
		err := s.db.Pool.QueryRow(ctx, s.q.GetAudio, c.AudioID, tenant).Scan(&a.ID, &a.TenantID, &a.Name, &a.FilePath, &a.Format, &a.SizeBytes, &a.CreatedAt)
		if err != nil {
			return p, errors.New("sound not found")
		}
		p.AudioFile, err = s.campaignAudio(ctx, tenant, a.FilePath, c.MaxSeconds)
		return p, err
	case "tts":
		if s.tts == nil || !s.tts.Enabled() {
			return p, errors.New("text-to-speech is unavailable; configure Piper and ffmpeg")
		}
		file, err := s.tts.Synthesize(ctx, c.Text, "")
		if err != nil {
			return p, errors.New("text-to-speech generation failed")
		}
		p.AudioFile, err = s.campaignAudio(ctx, tenant, file, c.MaxSeconds)
		return p, err
	case "ivr":
		var raw []byte
		var flowTenant uuid.UUID
		var flowName string
		if err := s.db.Pool.QueryRow(ctx, s.q.GetActiveFlowByID, c.FlowID, tenant).Scan(&flowTenant, &flowName, &raw); err != nil {
			return p, errors.New("active IVR flow not found")
		}
		flow, err := ivr.Parse(raw)
		if err != nil || flow == nil {
			return p, errors.New("invalid IVR flow")
		}
		if err = validateCampaignFlow(flow); err != nil {
			return p, err
		}
		for i := range flow.Nodes {
			n := &flow.Nodes[i]
			file := ivr.StringFromConfig(n.Config, "audio_file")
			text := ivr.StringFromConfig(n.Config, "greeting_text")
			if file == "" && text != "" {
				if strings.Contains(text, "{{") {
					return p, errors.New("campaign speech must use static text")
				}
				if s.tts == nil || !s.tts.Enabled() {
					return p, errors.New("IVR text requires a working TTS engine")
				}
				file, err = s.tts.Synthesize(ctx, text, "")
				if err != nil {
					return p, err
				}
			}
			if file != "" {
				file, err = s.campaignAudio(ctx, tenant, file, c.MaxSeconds)
				if err != nil {
					return p, fmt.Errorf("node %s: %w", n.ID, err)
				}
				n.Config["audio_file"] = file
			}
		}
		p.Flow = flow
	}
	return p, nil
}

func (s *Server) campaignTeam(ctx context.Context, tenant uuid.UUID, c campaign.Config) error {
	if c.Type != "agent" {
		return nil
	}
	var team Team
	if err := s.db.Pool.QueryRow(ctx, s.q.GetTeam, tenant, c.Team).Scan(
		&team.TenantID, &team.Name, &team.Strategy, &team.RingSeconds, &team.MaxWaitSeconds, &team.MohSound,
		&team.MinLiveAgents, &team.SLASeconds, &team.Hours, &team.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("select a team belonging to this tenant")
		}
		return err
	}
	return nil
}
func validateCampaignFlow(f *ivr.Flow) error {
	ids := map[string]bool{}
	for _, n := range f.Nodes {
		if n.ID == "" || ids[n.ID] {
			return errors.New("IVR node IDs must be unique")
		}
		ids[n.ID] = true
		switch n.Type {
		case ivr.NodeStart, ivr.NodeGreeting, ivr.NodeMenu, ivr.NodeGather, ivr.NodeTransfer, ivr.NodeHTTPCallback, ivr.NodeTiming, ivr.NodeHangup:
		default:
			return fmt.Errorf("unsupported campaign IVR node: %s", n.Type)
		}
	}
	if !ids[f.EntryNode] {
		return errors.New("IVR entry node is missing")
	}
	for _, e := range f.Edges {
		if !ids[e.From] || !ids[e.To] {
			return errors.New("IVR edge references a missing node")
		}
	}
	return nil
}

// campaignAudio always re-encodes, including Ogg uploads (which may contain
// Vorbis), and retains a private copy outside the deletable sound library.
func (s *Server) campaignAudio(ctx context.Context, tenant uuid.UUID, source string, maxSeconds int) (string, error) {
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", errors.New("audio file is missing")
	}
	root, err := filepath.EvalSymlinks(s.audioDir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, source)
	if err != nil || !filepath.IsLocal(rel) {
		return "", errors.New("audio must belong to this tenant")
	}
	top := strings.Split(filepath.ToSlash(rel), "/")[0]
	if top != tenant.String() && top != "tts" {
		return "", errors.New("audio must belong to this tenant")
	}
	dir := filepath.Join(root, tenant.String(), "campaigns")
	if err = os.MkdirAll(dir, 0750); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, uuid.NewString()+".ogg")
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-y", "-i", source, "-t", fmt.Sprint(maxSeconds+1), "-vn", "-ac", "1", "-ar", "48000", "-c:a", "libopus", "-frame_duration", "20", dest)
	if err = cmd.Run(); err != nil {
		_ = os.Remove(dest)
		return "", errors.New("audio conversion failed; ensure ffmpeg is installed and the file is valid")
	}
	if err = checkCampaignAudio(dest, maxSeconds); err != nil {
		_ = os.Remove(dest)
		return "", err
	}
	return dest, nil
}
func checkCampaignAudio(path string, maxSeconds int) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("prepared audio is missing; prepare the content again")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > 64<<20 {
		return errors.New("prepared audio exceeds the 64 MB playback limit")
	}
	frames, err := prompt.LoadOggOpus(f)
	if err != nil || len(frames) == 0 {
		return errors.New("audio has no playable Opus frames")
	}
	if len(frames) > maxSeconds*50 {
		return errors.New("audio exceeds the maximum call duration")
	}
	return nil
}
func (s *Server) checkCampaignPrepared(p *campaign.Prepared, maxSeconds int) error {
	if p == nil {
		return errors.New("prepare and preview campaign content before launch")
	}
	if p.Team != "" {
		return nil
	}
	if p.AudioFile != "" {
		return checkCampaignAudio(p.AudioFile, maxSeconds)
	}
	if p.Flow == nil {
		return errors.New("campaign has no prepared content")
	}
	for _, n := range p.Flow.Nodes {
		if file := ivr.StringFromConfig(n.Config, "audio_file"); file != "" {
			if err := checkCampaignAudio(file, maxSeconds); err != nil {
				return err
			}
		}
	}
	return nil
}
