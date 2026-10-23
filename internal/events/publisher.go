package events

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/shridarpatil/tring/internal/cache"
)

// Publisher fan-outs events on per-tenant Redis channels. Best-effort: a nil
// receiver is a no-op, and a Redis error is logged and dropped — publishing must
// never block or fail the call path.
type Publisher struct {
	rdb *redis.Client
	log *slog.Logger
}

// NewPublisher dials Redis at addr. An empty addr returns (nil, nil) — events
// disabled (no-op). A non-empty addr that can't be reached returns an error so
// the caller can log-and-disable.
func NewPublisher(addr, password string, logger *slog.Logger) (*Publisher, error) {
	if addr == "" {
		return nil, nil
	}
	rdb := redis.NewClient(cache.Options(addr, password))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Publisher{rdb: rdb, log: logger.With("component", "events.publisher")}, nil
}

// Publish encodes and publishes an event on the tenant's channel. Nil receiver =
// no-op.
func (p *Publisher) Publish(ctx context.Context, tenantID, typ string, data any) {
	if p == nil {
		return
	}
	payload, err := encode(tenantID, typ, data, time.Now().Unix())
	if err != nil {
		p.log.Warn("events: encode failed", "type", typ, "err", err)
		return
	}
	if err := p.rdb.Publish(ctx, Channel(tenantID), payload).Err(); err != nil {
		p.log.Warn("events: publish failed", "type", typ, "err", err)
	}
}

// Close releases the Redis client. Safe on a nil receiver.
func (p *Publisher) Close() error {
	if p == nil {
		return nil
	}
	return p.rdb.Close()
}
