package events

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestFrame(t *testing.T) {
	got := string(Frame("call.ended", []byte(`{"call_id":"x"}`)))
	want := "event: call.ended\ndata: {\"call_id\":\"x\"}\n\n"
	if got != want {
		t.Fatalf("Frame = %q, want %q", got, want)
	}
}

func TestChannel(t *testing.T) {
	if got := Channel("t1"); got != "tring:events:t1" {
		t.Fatalf("Channel = %q", got)
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	payload, err := encode("tenant-1", TypeCallRinging, map[string]any{"id": "c1"}, 1700000000)
	if err != nil {
		t.Fatal(err)
	}
	var e Event
	if err := json.Unmarshal(payload, &e); err != nil {
		t.Fatal(err)
	}
	if e.Type != TypeCallRinging || e.TenantID != "tenant-1" || e.TS != 1700000000 {
		t.Fatalf("bad envelope: %+v", e)
	}
	if !strings.Contains(string(e.Data), `"id":"c1"`) {
		t.Fatalf("bad data: %s", e.Data)
	}
}

func TestNilPublisherNoop(t *testing.T) {
	var p *Publisher
	p.Publish(context.Background(), "t", TypeCallEnded, nil) // must not panic
	if err := p.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
}
