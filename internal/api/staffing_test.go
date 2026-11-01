package api

import (
	"testing"
	"time"
)

func TestBelowMinTracker(t *testing.T) {
	var b belowMinTracker
	t1 := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	t2, t3, t4 := t1.Add(time.Minute), t1.Add(2*time.Minute), t1.Add(3*time.Minute)

	if got := b.observe("k", true, t1); got == nil || !got.Equal(t1) {
		t.Fatalf("first below = %v, want %v", got, t1)
	}
	if got := b.observe("k", true, t2); got == nil || !got.Equal(t1) {
		t.Fatalf("still below = %v, want %v (kept from first)", got, t1)
	}
	if got := b.observe("other", true, t2); got == nil || !got.Equal(t2) {
		t.Fatalf("other key = %v, want %v", got, t2)
	}
	if got := b.observe("k", false, t3); got != nil {
		t.Fatalf("recovered = %v, want nil", got)
	}
	if got := b.observe("k", true, t4); got == nil || !got.Equal(t4) {
		t.Fatalf("below again = %v, want %v", got, t4)
	}
}

// An agent on a call counts live even while Away (agentsList keeps it on_call);
// Away and offline/missing agents are members but not live.
func TestQueuesSnapshotStaffing(t *testing.T) {
	members := [][2]string{
		{"support", "a"}, {"support", "b"}, {"support", "c"}, {"support", "d"},
		{"sales", "a"},
	}
	status := map[string]string{"a": "on_call", "b": "online", "c": "away"}

	got := teamStaffing(members, status)
	if want := (staffCounts{Members: 4, Live: 2, OnCall: 1, Available: 1, Away: 1}); got["support"] != want {
		t.Fatalf("support = %+v, want %+v", got["support"], want)
	}
	if want := (staffCounts{Members: 1, Live: 1, OnCall: 1}); got["sales"] != want {
		t.Fatalf("sales = %+v, want %+v", got["sales"], want)
	}
}
