package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/events"
)

// dashboardSnapshot is the connect-time hydrate: all four panels at once. Sent as
// the first SSE frame (and on every reconnect) so the client starts consistent
// and self-heals any events missed while disconnected.
type dashboardSnapshot struct {
	Calls  []Call         `json:"calls"`
	Agents []Agent        `json:"agents"`
	Queues queuesResponse `json:"queues"`
	Stats  statsOverview  `json:"stats"`
}

func (s *Server) buildDashboardSnapshot(ctx context.Context, t *Tenant) dashboardSnapshot {
	snap := dashboardSnapshot{
		Calls:  []Call{},
		Agents: []Agent{},
		Queues: queuesResponse{Queues: []queueDepth{}},
		Stats:  statsOverview{Hourly: []statsHourBucket{}},
	}
	if s.db == nil {
		return snap
	}
	if c, err := s.activeCallsList(ctx, t.ID); err == nil {
		snap.Calls = c
	} else {
		s.log.Warn("snapshot: calls", "err", err)
	}
	agents, err := s.agentsList(ctx, t) // nil on error: staffing reads unknown
	if err == nil {
		snap.Agents = agents
	} else {
		s.log.Warn("snapshot: agents", "err", err)
	}
	if q, err := s.queuesSnapshot(ctx, t, agents); err == nil {
		snap.Queues = q
	} else {
		s.log.Warn("snapshot: queues", "err", err)
	}
	if st, err := s.statsOverview(ctx, t.ID); err == nil {
		snap.Stats = st
	} else {
		s.log.Warn("snapshot: stats", "err", err)
	}
	return snap
}

// handleDashboardStream is the dashboard live feed (SSE). It sends a full
// snapshot on connect, then forwards hub events for the caller's tenant, with a
// keepalive comment so proxies don't close the idle connection.
func (s *Server) handleDashboardStream(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx proxy buffering

	if t != nil {
		if snap, err := json.Marshal(s.buildDashboardSnapshot(r.Context(), t)); err == nil {
			_, _ = w.Write(events.Frame(events.TypeSnapshot, snap))
			flusher.Flush()
		}
	}

	var tenantID string
	if t != nil {
		tenantID = t.ID.String()
	}
	sub := s.hub.Subscribe(tenantID)
	defer s.hub.Unsubscribe(sub)

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-subFrames(sub):
			if !ok {
				return // hub dropped us (slow); client reconnects + re-snapshots
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			flusher.Flush()
		case <-ping.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// subFrames returns sub's channel, or a nil channel (blocks forever) when the
// hub is disabled — so the select still serves snapshot + keepalives.
func subFrames(sub *events.Subscriber) <-chan []byte {
	if sub == nil {
		return nil
	}
	return sub.Frames()
}

// StatsData / QueuesData are the hub recompute builders (registered in main).
func (s *Server) StatsData(ctx context.Context, tenantID string) ([]byte, error) {
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	ov, err := s.statsOverview(ctx, id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(ov)
}

func (s *Server) QueuesData(ctx context.Context, tenantID string) ([]byte, error) {
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	// agentsList needs the SIP domain (FreeSWITCH registration lookup).
	t := &Tenant{ID: id}
	if err := s.db.Pool.QueryRow(ctx, s.q.GetTenantSipDomain, id).Scan(&t.SipDomain); err != nil {
		return nil, err
	}
	agents, err := s.agentsList(ctx, t)
	if err != nil {
		s.log.Warn("queues recompute: agents", "err", err) // staffing reads unknown
	}
	q, err := s.queuesSnapshot(ctx, t, agents)
	if err != nil {
		return nil, err
	}
	return json.Marshal(q)
}
