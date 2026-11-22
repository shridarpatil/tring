package signaling

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/presence"
)

// wrapUp is an agent's timed after-call work: the answered call that ended,
// when the timer runs out, and whether its disposition is mandatory. While an
// agent has one they are connected but never a ring candidate. Keyed by agent
// ("tenant|sip"), not socket, so a page refresh keeps it. In-process like live
// calls: a gateway restart drops it and the agent reconnects online.
type wrapUp struct {
	callID  uuid.UUID
	endsAt  time.Time
	require bool
	timer   *time.Timer // nil until startWrapUp has done its writes
}

// wrapUpTimeout bounds each wrap-up DB call made on the call-end path.
const wrapUpTimeout = 3 * time.Second

// markLegsConnected records that roomID's call connected: every agent leg in
// it may wrap up when it ends, for the room's call row. Called where the call
// is answered — an agent's fresh join into a live call, or the far end
// picking up an outbound call the agent's leg was already in.
func (s *Server) markLegsConnected(roomID room.RoomID) {
	callID := uuid.Nil
	if s.cfg.Calls != nil {
		callID = s.cfg.Calls.CallID(string(roomID))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for pid, leg := range s.agentLegs {
		if leg.p.Room().ID() != roomID {
			continue
		}
		leg.connected = true
		if callID != uuid.Nil {
			leg.callID = callID
		}
		s.agentLegs[pid] = leg
	}
}

// setLegTransferred marks (or, when the call comes back to them, unmarks) an
// agent's leg as handed to someone else: an agent who transferred the call
// away has nothing to wrap up.
func (s *Server) setLegTransferred(pid room.ParticipantID, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if leg, ok := s.agentLegs[pid]; ok {
		leg.transferred = on
		s.agentLegs[pid] = leg
	}
}

// wrapUpSettings resolves the wrap-up for a leg that just ended: its team's
// seconds and mandatory-disposition flag (the tenant's when the leg came from
// no team). 0 seconds — wrap-up off, a call that never connected, or a failed
// lookup — means the agent goes straight back to available. Never call it
// under s.mu: it reads the DB.
func (s *Server) wrapUpSettings(leg agentLeg) (time.Duration, bool) {
	if !leg.connected || leg.transferred || leg.callID == uuid.Nil || s.cfg.Calls == nil {
		return 0, false
	}
	tid, err := uuid.Parse(leg.tenant)
	if err != nil {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), wrapUpTimeout)
	defer cancel()
	secs, require, err := s.cfg.Calls.WrapUpSettings(ctx, tid, leg.team)
	if err != nil {
		s.log.Warn("wrap-up settings lookup failed; skipping wrap-up", "tenant", leg.tenant, "sip", leg.sip, "team", leg.team, "err", err)
		return 0, false
	}
	return time.Duration(secs) * time.Second, require
}

// startWrapUp releases an ended agent leg into wrap-up for d. Like
// setAgentOnCall(false) it drops the leg from the on-call count, and a leg
// that still leaves the agent on another call starts nothing: only the last
// leg's call is wrapped up. The count drop and the wrap-up entry land under
// one lock, so no ring sees the agent free in between.
func (s *Server) startWrapUp(leg agentLeg, d time.Duration, require bool) {
	k := leg.tenant + "|" + leg.sip
	w := &wrapUp{callID: leg.callID, endsAt: time.Now().Add(d), require: require}
	s.mu.Lock()
	if s.onCall[k] > 0 {
		s.onCall[k]--
	}
	if s.onCall[k] > 0 {
		s.mu.Unlock()
		s.setAgentOnCallPresence(leg.tenant, leg.sip)
		return
	}
	delete(s.onCall, k)
	if s.wrapUps == nil {
		s.wrapUps = map[string]*wrapUp{}
	}
	prev := s.wrapUps[k]
	if prev != nil && prev.timer != nil {
		prev.timer.Stop()
	}
	s.wrapUps[k] = w
	c := s.agents[leg.tenant][leg.sip]
	s.mu.Unlock()

	tid, _ := uuid.Parse(leg.tenant) // valid: wrapUpSettings parsed it
	if prev != nil {
		// An older wrap-up still open (only reachable with presence off, where
		// click-to-call doesn't see it): close its row rather than leave it open.
		s.timeoutWrapUpRow(tid, prev.callID)
	}
	if pres := s.cfg.Presence; pres != nil {
		pres.MarkWrapUp(context.Background(), leg.tenant, leg.sip)
	}
	s.publishPresence(leg.tenant, leg.sip, presence.StatusWrapUp, time.Time{})
	ctx, cancel := context.WithTimeout(context.Background(), wrapUpTimeout)
	if err := s.cfg.Calls.BeginWrapUp(ctx, tid, w.callID, leg.sip, require); err != nil {
		s.log.Warn("wrap-up begin write failed", "tenant", leg.tenant, "sip", leg.sip, "call", w.callID, "err", err)
	}
	cancel()
	if c != nil {
		_ = c.send(wrapUpMsg(w))
	}

	// Arm the timer last, so it can't time out a row BeginWrapUp hasn't
	// written. A wrap-up already ended meanwhile (done, or a dial) has left
	// the map and gets no timer.
	s.mu.Lock()
	if s.wrapUps[k] == w {
		w.timer = time.AfterFunc(time.Until(w.endsAt), func() {
			s.finishWrapUp(leg.tenant, leg.sip, w.callID, w, true)
		})
	}
	s.mu.Unlock()
}

// endWrapUp ends sip's wrap-up of callID: saved (the API's wrap-up-done) or,
// with timedOut, closed unsaved (the agent placed a call). false when the
// agent has no wrap-up of that call here — a stale request is a no-op.
func (s *Server) endWrapUp(tenant, sip string, callID uuid.UUID, timedOut bool) bool {
	return s.finishWrapUp(tenant, sip, callID, nil, timedOut)
}

// finishWrapUp ends the agent's wrap-up if it is still for callID (and, when
// w is set, still that exact wrap-up: a timer must not end a newer one). The
// agent goes back to available — unless their socket is gone (offline: leave
// presence alone) or they are already on a new call.
func (s *Server) finishWrapUp(tenant, sip string, callID uuid.UUID, w *wrapUp, timedOut bool) bool {
	k := tenant + "|" + sip
	s.mu.Lock()
	cur := s.wrapUps[k]
	if cur == nil || cur.callID != callID || (w != nil && cur != w) {
		s.mu.Unlock()
		return false
	}
	delete(s.wrapUps, k)
	if cur.timer != nil {
		cur.timer.Stop()
	}
	c := s.agents[tenant][sip]
	away := s.away[k]
	busy := s.onCall[k] > 0
	s.mu.Unlock()

	if timedOut {
		tid, _ := uuid.Parse(tenant)
		s.timeoutWrapUpRow(tid, callID)
	}
	if c == nil {
		return true
	}
	if !busy {
		// Away is reported as it always is: online, which the dashboard and
		// the API show as away from available_for_calls. Their idle clock
		// restarts only when they come back (setAway), so no freed stamp.
		var freed time.Time
		if pres := s.cfg.Presence; pres != nil {
			pres.MarkOnline(context.Background(), tenant, sip)
			if !away {
				freed = pres.MarkFreed(context.Background(), tenant, sip)
			}
		}
		s.publishPresence(tenant, sip, presence.StatusOnline, freed)
	}
	_ = c.send(msg{Type: msgWrapUpEnd, CallID: callID.String()})
	return true
}

// endWrapUpForCall ends the agent's wrap-up, if any, as timed out: they are
// placing a new call, and are never in wrap-up and on a call at once.
func (s *Server) endWrapUpForCall(tenant, sip string) {
	s.mu.Lock()
	w := s.wrapUps[tenant+"|"+sip]
	s.mu.Unlock()
	if w != nil {
		s.endWrapUp(tenant, sip, w.callID, true)
	}
}

// timeoutWrapUpRow closes an unsaved wrap-up's call row (best-effort).
func (s *Server) timeoutWrapUpRow(tenant, callID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), wrapUpTimeout)
	defer cancel()
	if _, err := s.cfg.Calls.TimeoutWrapUp(ctx, tenant, callID); err != nil {
		s.log.Warn("wrap-up timeout write failed", "tenant", tenant, "call", callID, "err", err)
	}
}

// agentWrapUp returns the agent's wrap-up message, if they are in one.
func (s *Server) agentWrapUp(tenant, sip string) (msg, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.wrapUps[tenant+"|"+sip]
	if w == nil {
		return msg{}, false
	}
	return wrapUpMsg(w), true
}

func wrapUpMsg(w *wrapUp) msg {
	return msg{Type: msgWrapUp, CallID: w.callID.String(), EndsAt: w.endsAt.UTC().Format(time.RFC3339), RequireDisposition: w.require}
}

// handleControlWrapUpDone is POST /control/wrap-up-done {sip, call_id} → 204:
// the agent saved the call's wrap-up through the API, so it ends now instead
// of at its timer. 404 when this gateway has no wrap-up of that call for that
// agent in the token's tenant (already ended, or another gateway's agent).
func (s *Server) handleControlWrapUpDone(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.controlTenant(w, r)
	if !ok {
		return
	}
	var req struct {
		SIP    string    `json:"sip"`
		CallID uuid.UUID `json:"call_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || req.SIP == "" || req.CallID == uuid.Nil {
		controlError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !s.endWrapUp(tenant.String(), req.SIP, req.CallID, false) {
		controlError(w, http.StatusNotFound, "agent is not wrapping up that call")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
