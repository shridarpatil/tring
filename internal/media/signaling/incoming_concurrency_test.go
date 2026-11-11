package signaling_test

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/gobwas/ws/wsutil"
	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/media/signaling"
	"github.com/shridarpatil/tring/internal/presence"
)

func TestTwoInboundCallsDoNotDoubleRing(t *testing.T) {
	for _, redis := range []bool{false, true} {
		name := "memory"
		if redis {
			name = "redis"
		}
		t.Run(name, func(t *testing.T) {
			cfg := signaling.Config{Flows: transferFlows{}, Teams: fakeTeams{members: map[string][]string{"sales": {"alice"}}}}
			if redis {
				addr := os.Getenv("TRING_TEST_REDIS")
				if addr == "" {
					addr = "127.0.0.1:6379"
				}
				c, err := cache.New(addr, os.Getenv("TRING_TEST_REDIS_PASSWORD"))
				if err != nil {
					t.Skipf("Redis unavailable: %v", err)
				}
				t.Cleanup(func() { c.Close() })
				cfg.Cache = c
				cfg.Presence = presence.New(c)
			}
			ts := newServerCfg(t, cfg)
			tenant := uuid.New()
			alice := dial(t, ts.URL, mintToken(t, tenant, "alice"))
			alice.send(map[string]any{"type": "register"})
			alice.readUntil("ready")
			for _, room := range []string{"first", "second"} {
				caller := dial(t, ts.URL, mintToken(t, tenant, "visitor"))
				caller.send(map[string]any{"type": "join", "room": room, "flow_id": "f1", "name": "Caller"})
				caller.readUntil("joined")
				if room == "first" {
					if got := alice.readUntil("incoming"); got["room"] != "first" {
						t.Fatalf("first ring = %v", got)
					}
				}
			}
			alice.conn.SetReadDeadline(time.Now().Add(120 * time.Millisecond))
			for {
				data, err := wsutil.ReadServerText(alice.conn)
				if err != nil {
					var ne net.Error
					if !errors.As(err, &ne) || !ne.Timeout() {
						t.Fatalf("presence read failed: %v", err)
					}
					break
				}
				var m map[string]any
				if json.Unmarshal(data, &m) == nil && m["type"] == "incoming" {
					t.Fatalf("second call reached ringing agent: %v", m)
				}
			}
			alice.send(map[string]any{"type": "decline", "room": "first"})
			if got := alice.readUntil("incoming"); got["room"] != "second" {
				t.Fatalf("queued call did not ring after decline: %v", got)
			}
		})
	}
}
