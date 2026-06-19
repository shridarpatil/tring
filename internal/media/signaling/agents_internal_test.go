package signaling

import "testing"

// TestQueueDepthAggregates verifies the web-side depth counter sums multiple
// callers waiting for the same team (so phone+web aggregation is correct) and
// self-cleans when the queue empties.
func TestQueueDepthAggregates(t *testing.T) {
	s := &Server{queueDepth: make(map[depthKey]int)}
	dk := depthKey{tenant: "t1", team: "sales"}

	s.addQueueDepth(dk, 1)
	s.addQueueDepth(dk, 1) // a second concurrent web caller for the same team
	if got := s.queueDepth[dk]; got != 2 {
		t.Fatalf("depth = %d, want 2", got)
	}

	s.addQueueDepth(dk, -1)
	if got := s.queueDepth[dk]; got != 1 {
		t.Fatalf("depth after one leaves = %d, want 1", got)
	}

	s.addQueueDepth(dk, -1)
	if _, ok := s.queueDepth[dk]; ok {
		t.Fatal("an empty team must be dropped from the map, not left at 0")
	}

	// publishQueueDepth must be a safe no-op when no cache is configured.
	s.publishQueueDepth(t.Context(), dk)
}
