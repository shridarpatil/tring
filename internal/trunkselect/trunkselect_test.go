package trunkselect

import (
	"strings"
	"testing"
)

func TestSelectIndicesFailoverReturnsAllInOrder(t *testing.T) {
	got, err := SelectIndices(3, Failover, 0)
	if err != nil {
		t.Fatalf("SelectIndices: %v", err)
	}
	want := []int{0, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestSelectIndicesEmptyStrategyBehavesAsFailover(t *testing.T) {
	got, err := SelectIndices(3, "", 0)
	if err != nil {
		t.Fatalf("SelectIndices: %v", err)
	}
	want := []int{0, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestSelectIndicesManualReturnsFirstOnly(t *testing.T) {
	got, err := SelectIndices(3, Manual, 0)
	if err != nil {
		t.Fatalf("SelectIndices: %v", err)
	}
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("got %v, want [0]", got)
	}
}

func TestSelectIndicesRoundRobinUsesRRIndexModN(t *testing.T) {
	got, err := SelectIndices(2, RoundRobin, 1)
	if err != nil {
		t.Fatalf("SelectIndices: %v", err)
	}
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("rrIndex=1,n=2: got %v, want [1]", got)
	}

	got, err = SelectIndices(2, RoundRobin, 2)
	if err != nil {
		t.Fatalf("SelectIndices: %v", err)
	}
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("rrIndex=2,n=2: got %v, want [0]", got)
	}

	// Negative rrIndex: -1 % 2 == -1, then -1 + 2 == 1
	got, err = SelectIndices(2, RoundRobin, -1)
	if err != nil {
		t.Fatalf("SelectIndices: %v", err)
	}
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("rrIndex=-1,n=2: got %v, want [1]", got)
	}
}

func TestSelectIndicesEmptyReturnsNilNil(t *testing.T) {
	got, err := SelectIndices(0, Failover, 0)
	if err != nil {
		t.Fatalf("SelectIndices: %v", err)
	}
	if got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestSelectIndicesLoadBasedReturnsReservedError(t *testing.T) {
	_, err := SelectIndices(3, "load_based", 0)
	if err == nil {
		t.Fatal("expected error for load_based")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("error = %q, want it to contain 'reserved'", err.Error())
	}
}

func TestSelectIndicesUnknownStrategyReturnsError(t *testing.T) {
	_, err := SelectIndices(3, "bogus", 0)
	if err == nil {
		t.Fatal("expected error for unknown strategy")
	}
	if !strings.Contains(err.Error(), "unknown outbound_strategy") {
		t.Fatalf("error = %q, want it to contain 'unknown outbound_strategy'", err.Error())
	}
}

func TestSelectMapsIndicesBackToTrunks(t *testing.T) {
	trunks := []Trunk{
		{E164: "+918001111111", Name: "t_a"},
		{E164: "+918002222222", Name: "t_b"},
		{E164: "+918003333333", Name: "t_c"},
	}

	got, err := Select(trunks, Manual, 0)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(got) != 1 || got[0] != trunks[0] {
		t.Fatalf("manual: got %+v, want [%+v]", got, trunks[0])
	}

	got, err = Select(trunks, RoundRobin, 2)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(got) != 1 || got[0] != trunks[2] {
		t.Fatalf("round_robin rrIndex=2: got %+v, want [%+v]", got, trunks[2])
	}

	got, err = Select(trunks, Failover, 0)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("failover: got %+v, want all 3", got)
	}
	for i := range trunks {
		if got[i] != trunks[i] {
			t.Fatalf("failover order wrong at %d: got %+v, want %+v", i, got[i], trunks[i])
		}
	}
}
