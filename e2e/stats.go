//go:build e2e

package e2e

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// Arrival is one received RTP packet: when it arrived, which source sent it
// (the WebRTC stream id, e.g. the hold-music player or the customer's leg), its
// header fields, and its decoded audio.
type Arrival struct {
	At     time.Time
	Src    string
	Seq    uint16
	TS     uint32
	Marker bool
	PCM    []int16
}

// Recorder collects arrivals from a receive loop; safe for concurrent use.
type Recorder struct {
	mu sync.Mutex
	as []Arrival
}

func (r *Recorder) Add(a Arrival) {
	r.mu.Lock()
	r.as = append(r.as, a)
	r.mu.Unlock()
}

// Since returns a copy of the arrivals at or after t.
func (r *Recorder) Since(t time.Time) []Arrival {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Arrival
	for _, a := range r.as {
		if !a.At.Before(t) {
			out = append(out, a)
		}
	}
	return out
}

// Between returns the arrivals in [from, to).
func Between(as []Arrival, from, to time.Time) []Arrival {
	var out []Arrival
	for _, a := range as {
		if !a.At.Before(from) && a.At.Before(to) {
			out = append(out, a)
		}
	}
	return out
}

// waitArrivals waits until window has passed since from, then returns what r
// recorded in [from, from+window).
func waitArrivals(t *testing.T, r *Recorder, from time.Time, window time.Duration) []Arrival {
	t.Helper()
	time.Sleep(time.Until(from.Add(window)))
	return Between(r.Since(from), from, from.Add(window))
}

// Main returns the arrivals of the busiest source. Timing checks run per
// source: a browser agent gets hold music and the customer as separate tracks,
// each on its own clock, overlapping briefly at answer.
func Main(as []Arrival) []Arrival {
	count := map[string]int{}
	best := ""
	for _, a := range as {
		count[a.Src]++
		if count[a.Src] > count[best] {
			best = a.Src
		}
	}
	var out []Arrival
	for _, a := range as {
		if a.Src == best {
			out = append(out, a)
		}
	}
	return out
}

// CheckArrives: at least min packets landed within window of the first one.
func CheckArrives(as []Arrival, min int, window time.Duration) error {
	if len(as) == 0 {
		return fmt.Errorf("no packets arrived (want ≥%d in %v)", min, window)
	}
	n := len(Between(as, as[0].At, as[0].At.Add(window)))
	if n < min {
		return fmt.Errorf("%d packets arrived in %v, want ≥%d", n, window, min)
	}
	return nil
}

// CheckClock: from index 2 on (FreeSWITCH resets its timestamps on the second
// packet), consecutive timestamps step by step.
func CheckClock(as []Arrival, step uint32) error {
	for i := 2; i < len(as); i++ {
		if d := as[i].TS - as[i-1].TS; d != step {
			return fmt.Errorf("timestamp stepped by %d at packet %d (seq %d→%d), want %d", d, i, as[i-1].Seq, as[i].Seq, step)
		}
	}
	return nil
}

// CheckNoBurst: no window holds more than max packets — a drained backlog
// (stale queued audio) arrives as a burst well above real time. The error
// names the worst window.
func CheckNoBurst(as []Arrival, window time.Duration, max int) error {
	worst, at, j := 0, 0, 0
	for i := range as {
		for as[i].At.Sub(as[j].At) >= window {
			j++
		}
		if n := i - j + 1; n > worst {
			worst, at = n, j
		}
	}
	if worst > max {
		return fmt.Errorf("%d packets within %v from seq %d (a burst; want ≤%d)", worst, window, as[at].Seq, max)
	}
	return nil
}

// Hears reports the one tone clearly present in the arrivals' audio.
func Hears(as []Arrival, rate int, freqs []float64) (float64, bool) {
	var pcm []int16
	for _, a := range as {
		pcm = append(pcm, a.PCM...)
	}
	return Dominant(pcm, rate, freqs)
}
