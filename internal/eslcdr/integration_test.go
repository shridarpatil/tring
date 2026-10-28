//go:build integration

// Run with: go test -tags=integration ./internal/eslcdr/...
//
// Requires a reachable FreeSWITCH (ESL on TRING_INTEGRATION_FS_ADDR, default
// 127.0.0.1:8021, password TRING_INTEGRATION_FS_PASSWORD or ClueCon), as the
// CI integration job provides.

package eslcdr

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/percipia/eslgo"
	"github.com/percipia/eslgo/command"

	"github.com/shridarpatil/tring/internal/freeswitch"
)

func intAddr() string {
	if v := os.Getenv("TRING_INTEGRATION_FS_ADDR"); v != "" {
		return v
	}
	return "127.0.0.1:8021"
}

func intPassword() string {
	if v := os.Getenv("TRING_INTEGRATION_FS_PASSWORD"); v != "" {
		return v
	}
	return "ClueCon"
}

// TestTrackVarReachesChannelEvents checks the filter against a real
// FreeSWITCH: a channel originated with TrackVar (as the api does) carries it
// on CHANNEL_ANSWER and CHANNEL_HANGUP_COMPLETE, so the watcher still settles
// api calls, and a channel without it is skipped.
func TestTrackVarReachesChannelEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var (
		mu  sync.Mutex
		got = map[string]event{} // "<name> <uuid>" → parsed event
	)
	conn, err := eslgo.Dial(intAddr(), intPassword(), func() {})
	if err != nil {
		t.Fatalf("esl dial: %v", err)
	}
	defer conn.Close()
	conn.RegisterEventListener(eslgo.EventListenAll, func(e *eslgo.Event) {
		ev := toEvent(e)
		mu.Lock()
		got[ev.name+" "+ev.legID] = ev
		mu.Unlock()
	})
	if _, err := conn.SendCommand(ctx, command.Event{
		Format: "plain",
		Listen: []string{"CHANNEL_ANSWER", "CHANNEL_HANGUP_COMPLETE"},
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Both dial the echo extension (9999), which answers.
	fs := freeswitch.New(intAddr(), intPassword())
	tagged, untagged := uuid.NewString(), uuid.NewString()
	for _, c := range []struct {
		uuid string
		vars map[string]string
	}{
		{tagged, map[string]string{TrackVar: "true"}},
		{untagged, nil},
	} {
		if _, err := fs.Originate(ctx, freeswitch.OriginateRequest{
			ALeg:  "loopback/9999/default",
			BLegs: []string{"loopback/9999/default"},
			UUID:  c.uuid,
			Vars:  c.vars,
		}); err != nil {
			t.Fatalf("originate: %v", err)
		}
	}

	wait := func(key string) (event, bool) {
		for ctx.Err() == nil {
			mu.Lock()
			ev, ok := got[key]
			mu.Unlock()
			if ok {
				return ev, true
			}
			time.Sleep(100 * time.Millisecond)
		}
		return event{}, false
	}
	for _, id := range []string{tagged, untagged} {
		if _, ok := wait("CHANNEL_ANSWER " + id); !ok {
			t.Fatalf("no CHANNEL_ANSWER for %s", id)
		}
		if _, err := fs.Command(ctx, "uuid_kill", id); err != nil {
			t.Fatalf("uuid_kill %s: %v", id, err)
		}
	}

	for _, name := range []string{"CHANNEL_ANSWER", "CHANNEL_HANGUP_COMPLETE"} {
		ev, ok := wait(name + " " + tagged)
		if !ok {
			t.Fatalf("no %s for the tagged call", name)
		}
		if !ev.tracked {
			t.Errorf("%s of the tagged call lacks %s — the watcher would never settle api calls", name, TrackVar)
		}
		if ev, ok := wait(name + " " + untagged); !ok {
			t.Errorf("no %s for the untagged call", name)
		} else if ev.tracked {
			t.Errorf("%s of the untagged call is tracked", name)
		}
	}
}
