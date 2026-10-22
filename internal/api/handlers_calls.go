package api

import (
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
	"github.com/shridarpatil/tring/internal/freeswitch"
	"github.com/shridarpatil/tring/internal/provider"
	"github.com/shridarpatil/tring/internal/webhook"
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
// never sees the dialed number (the SDK has no API access; see ADR-001). The
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
		writeError(w, code, err.Error())
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
		return Call{}, http.StatusBadRequest, err
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
		return Call{}, http.StatusBadRequest, err
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
	originateVars := map[string]string{}
	if len(bLegs) > 1 && len(t.FailoverCauses) > 0 {
		originateVars["continue_on_fail"] = "'" + strings.Join(t.FailoverCauses, ",") + "'"
	}

	or, err := s.fs.Originate(ctx, freeswitch.OriginateRequest{
		ALeg:     aLeg,
		BLegs:    bLegs,
		UUID:     fsUUID.String(),
		CallerID: primaryDID,
		Vars:     originateVars,
	})
	if err != nil {
		return Call{}, http.StatusBadGateway, fmt.Errorf("originate failed: %w", err)
	}

	// from_number for the CDR is the primary DID (the one we expected
	// the call to come from). If failover kicked in and we actually
	// used a secondary, the orchestrator will (Phase 3) record which
	// trunk-leg was answered.
	outboundE164 := primaryDID

	var c Call
	// No pion room for the ESL/FreeSWITCH outbound path — room_id stays NULL
	// (nothing for a supervisor to join on this leg).
	err = s.db.Pool.QueryRow(ctx, s.q.CreateCallOutbound,
		t.ID, outboundE164, to, agentID, or.ChannelUUID, or.JobUUID, "").
		Scan(&c.ID, &c.TenantID, &c.Direction, &c.Status, &c.FromNumber, &c.ToNumber, &c.AgentID, &c.LegID, &c.StartedAt, &c.Source)
	if err != nil {
		return Call{}, http.StatusInternalServerError, fmt.Errorf("insert call: %w", err)
	}

	// Fire call.ringing webhook async (best-effort; can't block originate
	// on third-party endpoint latency).
	s.notifier.Emit(ctx, t.ID, webhook.EventCallRinging, callPayload(c))
	return c, 0, nil
}

type connectCallRequest struct {
	AgentNumber  string `json:"agent_number"`
	ClientNumber string `json:"client_number"`
}

// handleConnectCall bridges two PSTN numbers over the tenant's trunk: it
// rings agent_number first and, on answer, bridges client_number. Both legs
// present the trunk's DID as caller ID (regulatory: you can only present a
// number you own). This is the click-to-call / callback pattern — neither
// party needs a softphone, just a phone that rings.
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
	c, code, err := s.originateBridge(r.Context(), t, req.AgentNumber, req.ClientNumber)
	if err != nil {
		writeError(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// originateBridge rings agent_number and bridges client_number, both over the
// tenant's selected trunk. Returns (call, http_status_on_error, err).
func (s *Server) originateBridge(
	ctx context.Context,
	t *Tenant,
	agentNumber, clientNumber string,
) (Call, int, error) {
	trunks, err := s.selectTrunks(ctx, t)
	if err != nil {
		return Call{}, http.StatusBadRequest, err
	}

	// Normalize both numbers with the primary trunk's provider_type rules
	// (loopback passthrough in dev when no trunk is configured).
	primaryProvider := provider.For("loopback")
	if len(trunks) > 0 {
		primaryProvider = provider.For(trunks[0].ProviderType)
	}
	agentNorm, err := primaryProvider.NormalizeE164(agentNumber)
	if err != nil {
		return Call{}, http.StatusBadRequest, fmt.Errorf("invalid agent_number %q: %w", agentNumber, err)
	}
	clientNorm, err := primaryProvider.NormalizeE164(clientNumber)
	if err != nil {
		return Call{}, http.StatusBadRequest, fmt.Errorf("invalid client_number %q: %w", clientNumber, err)
	}

	aLeg, bLegs, callerID := buildBridgeLegs(trunks, agentNorm, clientNorm)

	fsUUID := uuid.New()
	or, err := s.fs.Originate(ctx, freeswitch.OriginateRequest{
		ALeg:     aLeg,
		BLegs:    bLegs,
		UUID:     fsUUID.String(),
		CallerID: callerID, // caller ID the agent's phone sees (the DID)
		Vars:     bridgeOriginateVars(),
	})
	if err != nil {
		return Call{}, http.StatusBadGateway, fmt.Errorf("originate failed: %w", err)
	}

	// from = agent (the party rung first), to = client (the destination).
	// agent_id is NULL — this path bridges raw numbers, not a SIP agent.
	var c Call
	err = s.db.Pool.QueryRow(ctx, s.q.CreateCallBridge,
		t.ID, agentNorm, clientNorm, or.ChannelUUID, or.JobUUID).
		Scan(&c.ID, &c.TenantID, &c.Direction, &c.Status, &c.FromNumber, &c.ToNumber, &c.AgentID, &c.LegID, &c.StartedAt, &c.Source)
	if err != nil {
		return Call{}, http.StatusInternalServerError, fmt.Errorf("insert call: %w", err)
	}

	s.notifier.Emit(ctx, t.ID, webhook.EventCallRinging, callPayload(c))
	return c, 0, nil
}

// bridgeOriginateVars are the originate (A-leg) variables for a two-number
// bridge. ignore_early_media makes FreeSWITCH wait for the agent's phone to
// really ANSWER before dialing the client: carriers like Tata answer every
// INVITE with 183 + early media (ringback/announcements), which FS otherwise
// treats as success — it dialed the client while the agent's phone hadn't
// even rung.
func bridgeOriginateVars() map[string]string {
	return map[string]string{"ignore_early_media": "true"}
}

// buildBridgeLegs constructs the originate legs for a two-PSTN-number bridge.
// A-leg rings the agent's phone; on answer FS bridges to the client. Both
// present the trunk's DID as caller ID. With no trunk (dev), falls back to
// loopback dial strings and no caller ID. Pure function — unit-tested for the
// exact dial-string shape FreeSWITCH expects.
func buildBridgeLegs(trunks []trunk, agentNumber, clientNumber string) (aLeg string, bLegs []string, callerID string) {
	if len(trunks) == 0 {
		lp := provider.For("loopback")
		return lp.DialString(agentNumber), []string{lp.DialString(clientNumber)}, ""
	}
	primary := trunks[0]
	aLeg = fmt.Sprintf("sofia/gateway/%s/%s", primary.Gateway, agentNumber)
	bLegs = []string{
		fmt.Sprintf(
			"[effective_caller_id_number=%s]sofia/gateway/%s/%s",
			primary.E164, primary.Gateway, clientNumber,
		),
	}
	return aLeg, bLegs, primary.E164
}

// callPayload is the data shape we ship in webhook envelopes. Mirrors the
// REST response so integrators can use one decoder for both surfaces.
func callPayload(c Call) map[string]interface{} { return callrow.Payload(c) }

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
	var owned bool
	if err := s.db.Pool.QueryRow(ctx, s.q.TenantOwnsNumber, tenant, callerID).Scan(&owned); err != nil {
		return fmt.Errorf("check caller_id: %w", err)
	}
	if !owned {
		return fmt.Errorf("caller_id %q is not one of this tenant's numbers", callerID)
	}
	return nil
}
