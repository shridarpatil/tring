package sfu_test

import (
	"testing"
	"time"

	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/media/sfu"
)

const supMark = 0x55

func (c *pktCollector) countMark(mark byte) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, p := range c.pkts {
		if len(p.Payload) > 0 && p.Payload[0] == mark {
			n++
		}
	}
	return n
}

// waitMark waits until the port has written at least n packets marked mark.
func waitMark(t *testing.T, c *pktCollector, mark byte, n int, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c.countMark(mark) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout: %s (got %d)", msg, c.countMark(mark))
}

// noNewMark asserts the port writes no further packets marked mark for a while.
func noNewMark(t *testing.T, c *pktCollector, mark byte, msg string) {
	t.Helper()
	time.Sleep(30 * time.Millisecond) // let an in-flight packet land
	before := c.countMark(mark)
	time.Sleep(150 * time.Millisecond)
	if got := c.countMark(mark); got != before {
		t.Fatalf("%s: %d new packets", msg, got-before)
	}
}

func newPortRoom(t *testing.T) (*sfu.SFU, *pktCollector) {
	t.Helper()
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	out := &pktCollector{}
	caller := &slowSource{id: "phone-leg", mark: 0xCC, total: 3000}
	if err := s.AttachPort("call", caller, out.write); err != nil {
		t.Fatalf("attach port: %v", err)
	}
	return s, out
}

func inject(t *testing.T, s *sfu.SFU, id room.ParticipantID, mark byte) {
	t.Helper()
	if err := s.InjectSource("call", &slowSource{id: id, mark: mark, total: 3000}, opusCap()); err != nil {
		t.Fatalf("inject %s: %v", id, err)
	}
}

// TestPortSupervisorModes: the phone caller never hears a supervisor in monitor
// or whisper. The agent publication is removed so the supervisor is the only
// feed the port could forward. Barge (which mixes) is covered by
// TestPortBargeAttachesAndDetaches.
func TestPortSupervisorModes(t *testing.T) {
	s, out := newPortRoom(t)
	inject(t, s, "agent", 0xBB)
	waitMark(t, out, 0xBB, 5, "port hears the agent")

	s.SetAudience("call", "sup", false, nil) // monitor, set before publishing
	inject(t, s, "sup", supMark)
	s.RemoveSource("call", "agent")
	noNewMark(t, out, supMark, "monitor reached the caller")
	if out.countMark(supMark) != 0 {
		t.Fatal("monitor reached the caller")
	}

	s.SetAudience("call", "sup", false, []room.ParticipantID{"agent"}) // whisper
	noNewMark(t, out, supMark, "whisper reached the caller")
	if out.countMark(supMark) != 0 {
		t.Fatal("whisper reached the caller")
	}
}

// TestPortSupervisorAudienceAfterPublish: a supervisor that published before its
// audience was set (so the port was attached) is cut off by SetAudience, and the
// port's feed goes to the next publisher.
func TestPortSupervisorAudienceAfterPublish(t *testing.T) {
	s, out := newPortRoom(t)
	inject(t, s, "sup", supMark)
	waitMark(t, out, supMark, 5, "unrestricted publisher reaches the port")

	inject(t, s, "agent", 0xBB)
	s.SetAudience("call", "sup", false, nil)
	noNewMark(t, out, supMark, "monitor set after publish still reaches the caller")
	waitMark(t, out, 0xBB, 5, "agent takes over the feed")
}

// TestPortAttachedAfterSupervisor: a port attached after a monitoring supervisor
// published must not be wired to it.
func TestPortAttachedAfterSupervisor(t *testing.T) {
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	inject(t, s, "agent", 0xBB) // creates the room
	s.SetAudience("call", "sup", false, nil)
	inject(t, s, "sup", supMark)
	s.RemoveSource("call", "agent")

	out := &pktCollector{}
	if err := s.AttachPort("call", &slowSource{id: "phone-leg", mark: 0xCC, total: 3000}, out.write); err != nil {
		t.Fatalf("attach port: %v", err)
	}
	noNewMark(t, out, supMark, "late port hears a monitoring supervisor")
	if out.countMark(supMark) != 0 {
		t.Fatal("late port hears a monitoring supervisor")
	}
}
