package signaling

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/gobwas/ws/wsutil"
	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/media/ivrdrive"
)

// fakeMask masks the SIPs set true; a SIP it doesn't know errors.
type fakeMask map[string]bool

func (f fakeMask) MaskNumbers(_ context.Context, _ uuid.UUID, sip string) (bool, error) {
	v, ok := f[sip]
	if !ok {
		return false, errors.New("unknown agent")
	}
	return v, nil
}

// firstIncoming reads c until an incoming arrives, then keeps draining so the
// ring's later writes (incoming-cancel) don't block.
func firstIncoming(t *testing.T, c net.Conn) <-chan msg {
	t.Helper()
	out := make(chan msg, 1)
	go func() {
		sent := false
		for {
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			data, err := wsutil.ReadServerText(c)
			if err != nil {
				return
			}
			var m msg
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			if m.Type == msgIncoming && !sent {
				out <- m
				sent = true
			}
		}
	}()
	return out
}

// A transfer ring shows a masked agent the caller's number masked and an
// unmasked one the full number; an agent whose verdict can't be read is
// masked (fail closed).
func TestIncomingMasksCallerNumberPerAgent(t *testing.T) {
	s := newHandoffServer(t)
	s.cfg.NumberMask = fakeMask{"alice": true, "bob": false}
	tenant := uuid.NewString()
	clients := map[string]net.Conn{}
	s.agents[tenant] = map[string]*conn{}
	for _, sip := range []string{"alice", "bob", "carol"} {
		serverConn, clientConn := net.Pipe()
		t.Cleanup(func() { _ = clientConn.Close(); _ = serverConn.Close() })
		s.agents[tenant][sip] = &conn{Conn: serverConn}
		clients[sip] = clientConn
	}
	got := map[string]<-chan msg{}
	for sip, c := range clients {
		got[sip] = firstIncoming(t, c)
	}

	_, eff, cleanup := s.Prepare(context.Background(), tenant+"/pstn-x",
		ivrdrive.Target{Label: "+919876543210"}, true, nil, "", "")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _, _ = eff.(*pionRing).Ring(ctx, []string{"alice", "bob", "carol"}, 200*time.Millisecond) }()

	want := map[string]string{"alice": "+91••••••3210", "bob": "+919876543210", "carol": "+91••••••3210"}
	for sip, w := range want {
		select {
		case m := <-got[sip]:
			if m.From != w {
				t.Errorf("%s incoming from = %q, want %q", sip, m.From, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no incoming", sip)
		}
	}
}
