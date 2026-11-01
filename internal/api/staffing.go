package api

import (
	"sync"
	"time"
)

// staffCounts is one team's members split by live state for the dashboard.
// Live = OnCall + Available: an agent on a call counts live even while Away.
// Unknown is set when any member's presence couldn't be read.
type staffCounts struct {
	Members, Live, OnCall, Available, Away int
	Unknown                                bool
}

// teamStaffing counts each team's members ({team, sip} pairs) by their
// agentsList status (sip → on_call | online | away | unknown | ...). Any other
// status, or none, counts only as a member; "unknown" marks the team Unknown.
func teamStaffing(members [][2]string, status map[string]string) map[string]staffCounts {
	out := map[string]staffCounts{}
	for _, m := range members {
		c := out[m[0]]
		c.Members++
		switch status[m[1]] {
		case "on_call":
			c.Live++
			c.OnCall++
		case "online":
			c.Live++
			c.Available++
		case "away":
			c.Away++
		case "unknown":
			c.Unknown = true
		}
		out[m[0]] = c
	}
	return out
}

// belowMinTracker remembers since when each "tenant|team" has had fewer live
// agents than its minimum while open. In-memory and per API process: a
// restart (or another replica) starts the clock again from its first look.
type belowMinTracker struct {
	mu    sync.Mutex
	since map[string]time.Time
}

// observe records whether key is below its minimum at now and returns the
// time it first went below, or nil once it is not.
func (b *belowMinTracker) observe(key string, below bool, now time.Time) *time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !below {
		delete(b.since, key)
		return nil
	}
	t, ok := b.since[key]
	if !ok {
		if b.since == nil {
			b.since = map[string]time.Time{}
		}
		t = now
		b.since[key] = t
	}
	return &t
}
