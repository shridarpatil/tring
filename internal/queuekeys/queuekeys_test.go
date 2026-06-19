package queuekeys

import "testing"

func TestDepthKeyDistinctPerSource(t *testing.T) {
	phone := DepthKey("t1", "sales", SourcePhone)
	web := DepthKey("t1", "sales", SourceWeb)
	if phone == web {
		t.Fatal("phone and web depth keys must differ so producers don't clobber each other")
	}
	if phone != "tring:queue:depth:t1:sales:phone" {
		t.Fatalf("phone key = %q, want tring:queue:depth:t1:sales:phone", phone)
	}
}

func TestSourcesCoversBothProducers(t *testing.T) {
	want := map[string]bool{SourcePhone: true, SourceWeb: true}
	if len(Sources) != len(want) {
		t.Fatalf("Sources = %v, want phone+web", Sources)
	}
	for _, s := range Sources {
		if !want[s] {
			t.Fatalf("unexpected source %q", s)
		}
	}
}
