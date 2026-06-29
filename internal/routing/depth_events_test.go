package routing

import (
	"context"
	"log/slog"
	"testing"

	"github.com/shridarpatil/tring/internal/events"
)

type depthSink struct {
	last map[string]any
	n    int
}

func (d *depthSink) Publish(_ context.Context, _, typ string, data any) {
	if typ == events.TypeQueueDepth {
		d.n++
		if m, ok := data.(map[string]any); ok {
			d.last = m
		}
	}
}

func TestPublishDepthEmitsEvent(t *testing.T) {
	sink := &depthSink{}
	r := NewRouter(NewMemQueue(), NewMemReservations(), NewMemRR(), nil, nil, sink, slog.Default())

	r.publishDepth(context.Background(), RouteConfig{TenantID: "tenant-1", Team: "sales", Source: "web"})

	if sink.n != 1 {
		t.Fatalf("got %d queue.depth events, want 1", sink.n)
	}
	if sink.last["team"] != "sales" || sink.last["source"] != "web" {
		t.Fatalf("bad event: %+v", sink.last)
	}
	if _, ok := sink.last["depth"].(int); !ok {
		t.Fatalf("depth not an int: %+v", sink.last)
	}
}

func TestPublishDepthSkipsWithoutTeam(t *testing.T) {
	sink := &depthSink{}
	r := NewRouter(NewMemQueue(), NewMemReservations(), NewMemRR(), nil, nil, sink, slog.Default())
	r.publishDepth(context.Background(), RouteConfig{TenantID: "t", Team: "", Source: "web"})
	if sink.n != 0 {
		t.Fatalf("expected no event without a team, got %d", sink.n)
	}
}
