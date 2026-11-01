package callcdr

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/events"
)

// queueStintEvent is the queue.stint payload: QueuedAt is set when the caller
// enters the team queue and null when they leave it.
type queueStintEvent struct {
	CallID     string     `json:"call_id"`
	Team       string     `json:"team"`
	QueuedAt   *time.Time `json:"queued_at"`
	SLASeconds int        `json:"sla_seconds"`
}

// stintTimeout bounds each stint write. The writes run on a context detached
// from the call's, since the leave typically follows the caller hanging up.
const stintTimeout = 3 * time.Second

// QueueEnter opens a queue stint for roomID's caller waiting in team, with the
// team's SLA snapshotted, and publishes queue.stint. call_id is NULL when the
// room has no live call. Best-effort: on error (or a nil Log / DB) it logs and
// returns uuid.Nil, and routing carries on.
func (l *Log) QueueEnter(ctx context.Context, roomID, team string, slaSeconds int) uuid.UUID {
	if l == nil || l.db == nil {
		return uuid.Nil
	}
	tenant, err := roomTenant(roomID)
	if err != nil {
		l.log.Warn("cdr: queue stint: bad room", "room", roomID, "err", err)
		return uuid.Nil
	}
	callID := l.CallID(roomID)
	var callArg *uuid.UUID
	if callID != uuid.Nil {
		callArg = &callID
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stintTimeout)
	defer cancel()
	var id uuid.UUID
	if err := l.db.Pool.QueryRow(ctx, l.q.InsertQueueStint, tenant, callArg, team, slaSeconds).Scan(&id); err != nil {
		l.log.Warn("cdr: queue stint insert failed", "room", roomID, "team", team, "err", err)
		return uuid.Nil
	}
	now := time.Now()
	l.publishStint(ctx, tenant, queueStintEvent{CallID: callIDString(callID), Team: team, QueuedAt: &now, SLASeconds: slaSeconds})
	return id
}

// QueueLeave closes stintID with outcome (answered | abandoned | timeout |
// transferred) and publishes queue.stint with queued_at null. Closing is
// idempotent. A uuid.Nil stint (QueueEnter failed) skips the SQL but still
// publishes, so the dashboard clears the waiting caller. A nil Log is a no-op.
func (l *Log) QueueLeave(ctx context.Context, stintID uuid.UUID, roomID, team, outcome string) {
	if l == nil {
		return
	}
	tenant, err := roomTenant(roomID)
	if err != nil {
		l.log.Warn("cdr: queue stint: bad room", "room", roomID, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stintTimeout)
	defer cancel()
	if stintID != uuid.Nil && l.db != nil {
		if _, err := l.db.Pool.Exec(ctx, l.q.EndQueueStint, stintID, outcome); err != nil {
			l.log.Warn("cdr: queue stint end failed", "stint", stintID, "err", err)
		}
	}
	l.publishStint(ctx, tenant, queueStintEvent{CallID: callIDString(l.CallID(roomID)), Team: team})
}

func (l *Log) publishStint(ctx context.Context, tenant uuid.UUID, ev queueStintEvent) {
	if l.events != nil {
		l.events.Publish(ctx, tenant.String(), events.TypeQueueStint, ev)
	}
}

// roomTenant parses the tenant from a "<tenant>/<name>" room id.
func roomTenant(roomID string) (uuid.UUID, error) {
	tenant, _, _ := strings.Cut(roomID, "/")
	return uuid.Parse(tenant)
}

// callIDString renders a call id for an event, "" when there is no call.
func callIDString(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}
