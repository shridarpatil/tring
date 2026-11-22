package callcdr

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shridarpatil/tring/internal/webhook"
)

// WrapUpWebhook sets the webhook sent call.wrapped_up once roomID's call wraps
// up — the flow's wrap-up webhook. Stored on the row (not in memory) because
// the wrap-up usually closes after the live call is gone. No-op for an unknown
// room.
func (l *Log) WrapUpWebhook(roomID string, webhookID uuid.UUID) {
	l.mu.Lock()
	ac := l.calls[roomID]
	l.mu.Unlock()
	if ac == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := l.db.Pool.Exec(ctx, l.q.SetCallWrapUpWebhook, ac.tenant, ac.id, webhookID); err != nil {
		l.log.Warn("cdr: set wrap-up webhook failed", "room", roomID, "err", err)
	}
}

// WrapUpSettings returns the wrap-up timer and whether a disposition is
// mandatory: the team's own values when team names an existing team, else the
// tenant's.
func (l *Log) WrapUpSettings(ctx context.Context, tenant uuid.UUID, team string) (seconds int, requireDisposition bool, err error) {
	if team != "" {
		err = l.db.Pool.QueryRow(ctx, l.q.GetTeamWrapUp, tenant, team).Scan(&seconds, &requireDisposition)
		if !errors.Is(err, pgx.ErrNoRows) {
			return seconds, requireDisposition, err
		}
	}
	err = l.db.Pool.QueryRow(ctx, l.q.GetTenantWrapUp, tenant).Scan(&seconds, &requireDisposition)
	return seconds, requireDisposition, err
}

// BeginWrapUp records which agent is wrapping up callID and whether a
// disposition is mandatory, as the settings stood when wrap-up began.
func (l *Log) BeginWrapUp(ctx context.Context, tenant, callID uuid.UUID, agentSIP string, requireDisposition bool) error {
	agentID, _, err := l.agentBySIP(ctx, tenant, agentSIP)
	if err != nil {
		return err
	}
	_, err = l.db.Pool.Exec(ctx, l.q.BeginCallWrapUp, tenant, callID, agentID, requireDisposition)
	return err
}

// TimeoutWrapUp closes callID's wrap-up as timed out. won is false when the
// agent's save got there first (the row was already closed); only the winner
// sends call.wrapped_up, so the event fires once.
func (l *Log) TimeoutWrapUp(ctx context.Context, tenant, callID uuid.UUID) (won bool, err error) {
	var hook *uuid.UUID
	if err := l.db.Pool.QueryRow(ctx, l.q.TimeoutCallWrapUp, tenant, callID).Scan(&hook); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if hook != nil {
		l.emitCallID(ctx, tenant, callID, *hook, webhook.EventCallWrappedUp)
	}
	return true, nil
}
