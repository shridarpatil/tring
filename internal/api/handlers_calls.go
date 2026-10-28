package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shridarpatil/tring/internal/agentleg"
	"github.com/shridarpatil/tring/internal/callrow"
	"github.com/shridarpatil/tring/internal/dial"
	"github.com/shridarpatil/tring/internal/eslcdr"
	"github.com/shridarpatil/tring/internal/freeswitch"
	"github.com/shridarpatil/tring/internal/provider"
)

// Call is a call-log row; the shared definition lives in callrow so the
// gateway's call.answered / call.ended webhooks carry the identical object.
type Call = callrow.Call

type createCallRequest struct {
	AgentID uuid.UUID `json:"agent_id"`
	To      string    `json:"to"`
	// AgentLabel is the non-PII string shown to the agent for this call (e.g. a
	// ticket/account ref). The agent NEVER receives the dialed number — the
	// backend holds it. Optional; empty ⇒ the agent sees a generic "Incoming call".
	AgentLabel string `json:"agent_label"`
	// CallerID is the number the callee sees, overriding the trunk's own DID —
	// e.g. the support number for a masked callback. Must be one of the tenant's
	// active numbers. Optional; empty ⇒ the trunk's DID.
	CallerID string `json:"caller_id"`
}

// handleCreateCall is the originate surface. The CRM's backend calls this with
// its tenant API key, choosing the agent + destination — the agent's browser
// never sees the dialed number (the SDK cannot originate; see ADR-001). The
// agent leg auto-answers (the agent already clicked to call).
func (s *Server) handleCreateCall(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req createCallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.AgentID == uuid.Nil || req.To == "" {
		writeError(w, http.StatusBadRequest, "agent_id and to are required")
		return
	}
	c, code, err := s.originateForAgent(r.Context(), t, req.AgentID, req.To, req.AgentLabel, req.CallerID)
	if err != nil {
		s.writeCallError(w, r, code, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// agentALeg builds the FreeSWITCH dial string for an agent's own call leg via
// the shared agentleg helper. AutoAnswer is set because this is the backend
// click-to-call path — the gateway auto-answers the agent's browser. Attribute
// stays off (the API already knows which agent it dialed); the orchestrator's
// inbound-transfer path sets Attribute instead. Both share one definition so
// the leg is identical however the call arrived.
func (s *Server) agentALeg(sipUsername, sipDomain, tenantID, label string) string {
	return agentleg.Build(agentleg.Options{
		Transport:  s.agentTransport,
		Gateway:    s.agentGateway,
		SIP:        sipUsername,
		Domain:     sipDomain,
		TenantID:   tenantID,
		Label:      label,
		AutoAnswer: true,
		JitterMsec: s.agentJitter,
	})
}

// errInternal marks an error from the originate path that is a server-side
// failure (DB, I/O) rather than a problem with the caller's input. statusFor
// maps it to 500; writeCallError then keeps its detail out of the response.
var errInternal = errors.New("internal error")

// statusFor is 500 for an errInternal-wrapped error, else 400.
func statusFor(err error) int {
	if errors.Is(err, errInternal) {
		return http.StatusInternalServerError
	}
	return http.StatusBadRequest
}

// writeCallError answers a failed originate. 4xx errors describe the caller's
// input (bad destination, unowned caller_id, unknown agent) and are returned
// as-is; 5xx/502 carry pgx errors or FreeSWITCH replies, so the client gets a
// generic message and the detail is logged.
func (s *Server) writeCallError(w http.ResponseWriter, r *http.Request, code int, err error) {
	switch {
	case code == http.StatusBadGateway:
		s.writeServerError(w, r, code, "originate failed", err)
	case code >= 500:
		s.writeServerError(w, r, code, "internal error", err)
	default:
		writeError(w, code, err.Error())
	}
}

// originateForAgent is the shared originate path used by both the tenant-
// scoped and JWT-scoped handlers. Returns (call, http_status_on_error, err).
func (s *Server) originateForAgent(
	ctx context.Context,
	t *Tenant,
	agentID uuid.UUID,
	to string,
	agentLabel string,
	callerID string,
) (Call, int, error) {
	if err := s.checkCallerID(ctx, t.ID, callerID); err != nil {
		return Call{}, statusFor(err), err
	}

	var sipUsername string
	err := s.db.Pool.QueryRow(ctx, s.q.GetAgentSipUsername,
		agentID, t.ID,
	).Scan(&sipUsername)
	if errors.Is(err, pgx.ErrNoRows) {
		return Call{}, http.StatusNotFound, errors.New("agent not found")
	}
	if err != nil {
		return Call{}, http.StatusInternalServerError, errors.New("agent lookup failed")
	}

	// Pick the trunk(s) to use based on the tenant's outbound strategy.
	// May return zero trunks (dev: fall back to loopback) or many
	// (failover: pipe-join in priority order).
	trunks, err := s.selectTrunks(ctx, t)
	if err != nil {
		return Call{}, statusFor(err), err
	}

	// Normalize using the primary trunk's provider_type. All trunks for
	// a tenant target the same country today; when cross-country trunks
	// land in Phase 2, this becomes a per-trunk loop.
	primaryProvider := provider.For("loopback")
	if len(trunks) > 0 {
		primaryProvider = provider.For(trunks[0].ProviderType)
	}
	normalized, err := primaryProvider.NormalizeE164(to)
	if err != nil {
		return Call{}, http.StatusBadRequest, fmt.Errorf("invalid destination %q: %w", to, err)
	}
	to = normalized

	fsUUID := uuid.New()

	sipDomain := t.SipDomain
	if sipDomain == "" {
		_ = s.db.Pool.QueryRow(ctx, s.q.GetTenantSipDomain, t.ID).Scan(&sipDomain)
	}
	aLeg := s.agentALeg(sipUsername, sipDomain, t.ID.String(), agentLabel)

	// Build BLegs. Each entry presents that trunk's own DID as caller-ID
	// via per-leg `[var=val]` prefix, so a roll-over from Airtel→Jio
	// doesn't try to present Airtel's DID over the Jio trunk (which
	// would fail caller-ID-validation at Jio's side).
	bLegs := make([]string, 0, max(1, len(trunks)))
	primaryDID := ""
	if len(trunks) == 0 {
		bLegs = append(bLegs, provider.For("loopback").DialString(to))
	} else {
		primaryDID = trunks[0].E164
		if callerID != "" {
			primaryDID = callerID
		}
		for _, tr := range trunks {
			dial := fmt.Sprintf("sofia/gateway/%s/%s", tr.Gateway, to)
			cid := tr.E164
			if callerID != "" {
				cid = callerID // explicit override applies on every trunk leg
			}
			bLegs = append(bLegs,
				fmt.Sprintf("[effective_caller_id_number=%s]%s", cid, dial),
			)
		}
	}

	// If there are 2+ B-legs, set `continue_on_fail` per tenant config so
	// FS rolls to the next trunk only on the causes the operator opted in
	// for. Single-quote-wrapped so the commas inside don't terminate the
	// `{}` vars block.
	// TrackVar lets eslcdr pick this call's events out of every channel's.
	originateVars := map[string]string{eslcdr.TrackVar: "true"}
	if len(bLegs) > 1 && len(t.FailoverCauses) > 0 {
		originateVars["continue_on_fail"] = "'" + strings.Join(t.FailoverCauses, ",") + "'"
	}

	// from_number for the CDR is the primary DID (the one we expected
	// the call to come from). If failover kicked in and we actually
	// used a secondary, the orchestrator will (Phase 3) record which
	// trunk-leg was answered.
	outboundE164 := primaryDID

	// Row first, then originate (see OriginateRequest.JobUUID). No pion room
	// for the ESL/FreeSWITCH outbound path — room_id stays NULL (nothing for
	// a supervisor to join on this leg).
	jobUUID := uuid.NewString()
	var c Call
	err = s.db.Pool.QueryRow(ctx, s.q.CreateCallOutbound,
		t.ID, outboundE164, to, agentID, fsUUID.String(), jobUUID, "").
		Scan(&c.ID, &c.TenantID, &c.Direction, &c.Status, &c.FromNumber, &c.ToNumber, &c.AgentID, &c.LegID, &c.StartedAt, &c.Source)
	if err != nil {
		return Call{}, http.StatusInternalServerError, fmt.Errorf("insert call: %w", err)
	}

	_, err = s.fs.Originate(ctx, freeswitch.OriginateRequest{
		ALeg:     aLeg,
		BLegs:    bLegs,
		UUID:     fsUUID.String(),
		CallerID: primaryDID,
		Vars:     originateVars,
		JobUUID:  jobUUID,
	})
	if err != nil {
		s.failOriginate(ctx, jobUUID)
		return Call{}, http.StatusBadGateway, fmt.Errorf("originate failed: %w", err)
	}

	return c, 0, nil
}

// failOriginate marks a call row failed when FreeSWITCH never accepted its
// originate, so it doesn't sit in "ringing" forever.
func (s *Server) failOriginate(ctx context.Context, jobUUID string) {
	if _, err := s.db.Pool.Exec(ctx, s.q.FailCallByJob, "ORIGINATE_FAILED", jobUUID); err != nil {
		s.log.Error("mark originate failed", "job_uuid", jobUUID, "err", err)
	}
}

type connectCallRequest struct {
	AgentNumber  string `json:"agent_number"`
	ClientNumber string `json:"client_number"`
}

// handleConnectCall connects two PSTN numbers over the tenant's trunk: it
// rings agent_number first and, on answer, client_number, both presenting the
// trunk's DID as caller ID (regulatory: you can only present a number you
// own). This is the click-to-call / callback pattern — neither party needs a
// softphone, just a phone that rings. The media gateway places the call
// (signaling/connect.go), so it's recorded and tracked like every other call;
// the response is the call row, still ringing.
//
//	curl -X POST .../v1/calls/connect -H "Authorization: Bearer $KEY" \
//	  -d '{"agent_number":"+919876543210","client_number":"+919812345678"}'
func (s *Server) handleConnectCall(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req connectCallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.AgentNumber == "" || req.ClientNumber == "" {
		writeError(w, http.StatusBadRequest, "agent_number and client_number are required")
		return
	}
	callID, code, msg := s.gatewayConnect(r.Context(), t.ID, req)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	c, err := scanCall(s.db.Pool.QueryRow(r.Context(), s.q.GetCall, callID, t.ID))
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// gatewayConnect asks the media gateway to place a connect call and returns
// its call id, or the status + message to answer with. The gateway's own 4xx
// (bad number, no trunk) pass through; anything else is a 502.
func (s *Server) gatewayConnect(ctx context.Context, tenantID uuid.UUID, req connectCallRequest) (uuid.UUID, int, string) {
	tok, err := s.issueControlToken(tenantID)
	if err != nil {
		s.log.Error("connect: sign control token", "err", err)
		return uuid.Nil, http.StatusInternalServerError, "internal error"
	}
	body, _ := json.Marshal(req)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.gatewayControlURL+"/control/connect", bytes.NewReader(body))
	if err != nil {
		s.log.Error("connect: build gateway request", "err", err)
		return uuid.Nil, http.StatusInternalServerError, "internal error"
	}
	hreq.Header.Set("Authorization", "Bearer "+tok)
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := s.gatewayHTTP.Do(hreq)
	if err != nil {
		s.log.Error("connect: gateway unreachable", "url", s.gatewayControlURL, "err", err)
		return uuid.Nil, http.StatusBadGateway, "media gateway unavailable"
	}
	defer resp.Body.Close()
	var out struct {
		CallID uuid.UUID `json:"call_id"`
		Error  string    `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out)
	switch {
	case resp.StatusCode == http.StatusCreated && out.CallID != uuid.Nil:
		return out.CallID, 0, ""
	case resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusUnauthorized:
		return uuid.Nil, resp.StatusCode, out.Error
	default:
		s.log.Error("connect: gateway refused", "status", resp.StatusCode, "error", out.Error)
		return uuid.Nil, http.StatusBadGateway, "call not placed"
	}
}

const (
	callsDefaultLimit = 50
	callsMaxLimit     = 200

	// defaultPageSize / maxPageSize bound the numbered-pagination window shared
	// by every paginated list endpoint (see pageParams).
	defaultPageSize = 25
	maxPageSize     = callsMaxLimit
)

func scanCall(scanner interface{ Scan(...interface{}) error }) (Call, error) {
	return callrow.Scan(scanner)
}

func (s *Server) handleGetCall(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	row := s.db.Pool.QueryRow(r.Context(), s.q.GetCall, id, t.ID)
	c, err := scanCall(row)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "call not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// callEvent is one row of a call's durable timeline (call_events). Detail is
// type-specific JSON passed through verbatim.
type callEvent struct {
	Type   string          `json:"type"`
	At     time.Time       `json:"at"`
	Detail json.RawMessage `json:"detail"`
}

// handleGetCallEvents returns a call's complete, ordered event timeline — the
// console's per-call history (started → rings/no-answers → answered → transfers →
// ended). Tenant-scoped so one tenant can't read another's history. Returns an
// empty array (not 404) for a call with no recorded events, so the UI renders
// consistently for old calls that predate the timeline.
func (s *Server) handleGetCallEvents(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), s.q.ListCallEvents, id, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	out := []callEvent{}
	for rows.Next() {
		var e callEvent
		var detail []byte
		if err := rows.Scan(&e.Type, &e.At, &detail); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		e.Detail = detail
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, out)
}

// recordingMIME maps a recording file extension to its Content-Type. Explicit
// (not mime.TypeByExtension) because the stdlib table lacks ".wav" on some
// platforms. Mirrors the orchestrator's mapping in runner.go.
func recordingMIME(ext string) string {
	switch strings.ToLower(ext) {
	case ".mp3":
		return "audio/mpeg"
	case ".opus", ".ogg":
		return "audio/ogg"
	default:
		return "audio/wav"
	}
}

// handleGetCallRecording streams a call's recording. Tenant-scoped: the
// leg_id lookup is filtered by tenant so one tenant can't fetch another's
// recording. The file is written by FreeSWITCH to the shared recordings mount,
// named by leg_id. 404 if the call isn't ours or the file isn't there yet
// (recording still in progress / never recorded).
func (s *Server) handleGetCallRecording(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var (
		fsUUID string
		s3Key  *string
	)
	err = s.db.Pool.QueryRow(r.Context(), s.q.GetCallRecording, id, t.ID).Scan(&fsUUID, &s3Key)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "call not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	// Stored in S3: stream it through (same-origin, header auth — no presign, no
	// CORS, no token in the URL).
	if s3Key != nil && *s3Key != "" && s.blob.Enabled() {
		obj, err := s.blob.Get(r.Context(), *s3Key)
		if err != nil {
			writeError(w, http.StatusNotFound, "recording not available")
			return
		}
		defer obj.Close()
		// Content-Type from the stored key's extension (mp3/wav/opus), not a
		// hardcoded WAV — the format is configurable and may differ per object.
		w.Header().Set("Content-Type", recordingMIME(filepath.Ext(*s3Key)))
		_, _ = io.Copy(w, obj)
		return
	}

	// Local spool fallback (S3 disabled, or not yet uploaded). The gateway writes
	// <leg_id>.<format> in the tenant's format at the time of the call
	// (internal/callcdr), so look for each; http.ServeFile sets Content-Type.
	for _, format := range recordingFormats {
		path := filepath.Join(s.recordingsDir, fsUUID+"."+format)
		if _, err := os.Stat(path); err == nil {
			http.ServeFile(w, r, path)
			return
		}
	}
	writeError(w, http.StatusNotFound, "recording not available")
}

// activeCallsList returns in-progress calls (ringing/answered) for a tenant,
// oldest first — the cockpit "Live calls" panel and the SSE snapshot share it.
func (s *Server) activeCallsList(ctx context.Context, tenantID uuid.UUID) ([]Call, error) {
	rows, err := s.db.Pool.Query(ctx, s.q.ListActiveCalls, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Call{}
	for rows.Next() {
		c, err := scanCall(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *Server) handleListCalls(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	status := r.URL.Query().Get("status")

	// active=true: the cockpit "Live calls" panel wants only in-progress calls
	// (ended_at IS NULL, ringing/answered), oldest first, unpaginated. Bypasses
	// the page/status machinery below — it's its own bounded query.
	if r.URL.Query().Get("active") == "true" {
		out, err := s.activeCallsList(r.Context(), t.ID)
		if err != nil {
			s.log.Error("list active calls failed", "err", err)
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		writeJSON(w, http.StatusOK, pageEnvelope{Items: out, Total: len(out), Limit: len(out), Offset: 0})
		return
	}

	limit, offset, paged := pageParams(r)
	if !paged {
		// No page param: keep the legacy default volume (first callsDefaultLimit,
		// no offset), still honouring an explicit ?limit (e.g. the dashboard's 8).
		limit = callsDefaultLimit
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		if limit > callsMaxLimit {
			limit = callsMaxLimit
		}
		offset = 0
	}

	// One parameterised query pair for every filter combination ('' = off).
	idPrefix, digits := callSearch(r.URL.Query().Get("q"))
	countQuery, countArgs := s.q.CountCalls, []interface{}{t.ID, status, idPrefix, digits}
	listQuery, listArgs := s.q.ListCalls, []interface{}{t.ID, limit, offset, status, idPrefix, digits}

	var total int
	if err := s.db.Pool.QueryRow(r.Context(), countQuery, countArgs...).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "count failed")
		return
	}

	rows, err := s.db.Pool.Query(r.Context(), listQuery, listArgs...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	out := []Call{}
	for rows.Next() {
		c, err := scanCall(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, pageEnvelope{Items: out, Total: total, Limit: limit, Offset: offset})
}

// callSearch splits the call-log search box into phone digits or a call-id
// prefix. Input made only of phone characters (digits, space, + - ( ) .) is a
// number: formatting is stripped and a national leading 0 dropped, so
// "09741…", "+91 97410-…" and "9741…" all match a stored "+91…"/"91…".
// Anything else is a call id (calls.id or the leg id), matched as a prefix
// so a shortened id works. Digits and hyphens alone, at least as long as a
// UUID's first block, could be either (an id like "15693408-…"), so both are
// returned and a call matching either is found.
func callSearch(q string) (idPrefix, digits string) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", ""
	}
	if strings.Trim(q, "0123456789 +-().") != "" {
		// Keep only id characters: no LIKE wildcards (% _) from the user.
		var id strings.Builder
		for _, r := range strings.ToLower(q) {
			if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || r == '-' {
				id.WriteRune(r)
			}
		}
		return id.String(), ""
	}
	var b strings.Builder
	for _, r := range q {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if len(q) >= 8 && strings.Trim(q, "0123456789-") == "" {
		idPrefix = q
	}
	return idPrefix, strings.TrimLeft(b.String(), "0")
}

// checkCallerID accepts an empty caller ID (use the trunk's DID) or one of the
// tenant's active numbers, so a tenant can only present numbers it holds.
func (s *Server) checkCallerID(ctx context.Context, tenant uuid.UUID, callerID string) error {
	if callerID == "" {
		return nil
	}
	if !dial.IsNumber(callerID) {
		return fmt.Errorf("caller_id %q is not a valid number", callerID)
	}
	var owned bool
	if err := s.db.Pool.QueryRow(ctx, s.q.TenantOwnsNumber, tenant, callerID).Scan(&owned); err != nil {
		return fmt.Errorf("check caller_id: %w: %w", errInternal, err)
	}
	if !owned {
		return fmt.Errorf("caller_id %q is not one of this tenant's numbers", callerID)
	}
	return nil
}
