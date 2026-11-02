//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// paced builds n arrivals 20 ms apart with timestamps stepping by step.
func paced(n int, step uint32) []Arrival {
	t0 := time.Unix(1000, 0)
	out := make([]Arrival, n)
	for i := range out {
		out[i] = Arrival{At: t0.Add(time.Duration(i) * 20 * time.Millisecond), Seq: uint16(i), TS: uint32(i) * step}
	}
	return out
}

func TestCheckClock(t *testing.T) {
	as := paced(10, 960)
	as[0].TS = 777777 // FreeSWITCH's odd first packet; packet 1 starts the real clock
	if err := CheckClock(as, 960); err != nil {
		t.Errorf("960 steps from index 2: %v", err)
	}
	bad := paced(10, 160)
	err := CheckClock(bad, 960)
	if err == nil || !strings.Contains(err.Error(), "160") {
		t.Errorf("160 steps: err = %v, want an error naming 160", err)
	}
}

func TestCheckNoBurst(t *testing.T) {
	if err := CheckNoBurst(paced(100, 960), 200*time.Millisecond, 15); err != nil {
		t.Errorf("20 ms spacing: %v", err)
	}
	burst := paced(100, 960)
	for i := 0; i < 30; i++ { // a backlog drained at once
		burst[i].At = burst[0].At.Add(time.Duration(i) * time.Millisecond)
	}
	err := CheckNoBurst(burst, 200*time.Millisecond, 15)
	if err == nil || !strings.Contains(err.Error(), "30") {
		t.Errorf("30 packets in 30 ms: err = %v, want an error naming the count", err)
	}
}

func TestCheckArrives(t *testing.T) {
	if err := CheckArrives(paced(100, 960), 50, 2*time.Second); err != nil {
		t.Errorf("100 packets over 2 s: %v", err)
	}
	err := CheckArrives(paced(10, 960), 50, 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "10") {
		t.Errorf("10 packets: err = %v, want an error naming the count", err)
	}
}

func TestRecorderSince(t *testing.T) {
	r := &Recorder{}
	for _, a := range paced(10, 960) {
		r.Add(a)
	}
	if got := len(r.Since(time.Unix(1000, 0).Add(100 * time.Millisecond))); got != 5 {
		t.Errorf("Since(+100ms) = %d arrivals, want 5", got)
	}
}
