package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// roomID mimics room.RoomID: a named string type, which slog keeps as KindAny.
type roomID string

func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(callHandler{h: slog.NewTextHandler(buf, nil)})
}

func TestCallAddedForRegisteredRoom(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	SetCall("t1/pstn-a", "call-1")
	defer ClearCall("t1/pstn-a", "call-1")

	log.Info("ivr started", "room", roomID("t1/pstn-a"))
	if !strings.Contains(buf.String(), "call=call-1") {
		t.Fatalf("want call=call-1 in %q", buf.String())
	}
}

func TestNoCallForUnknownRoomOrNoRoom(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)

	log.Info("ivr started", "room", "t1/unknown")
	log.Info("startup")
	if strings.Contains(buf.String(), "call=") {
		t.Fatalf("unexpected call= in %q", buf.String())
	}
}

func TestExistingCallNotDuplicated(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	SetCall("t1/pstn-b", "call-2")
	defer ClearCall("t1/pstn-b", "call-2")

	log.Info("cdr: call started", "room", "t1/pstn-b", "call", "call-2")
	if n := strings.Count(buf.String(), "call="); n != 1 {
		t.Fatalf("want one call= attr, got %d in %q", n, buf.String())
	}
}

func TestRoomFromWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf).With("room", roomID("t1/web-c"), "participant", "p1")
	SetCall("t1/web-c", "call-3")
	defer ClearCall("t1/web-c", "call-3")

	log.Info("source injected")
	if !strings.Contains(buf.String(), "call=call-3") {
		t.Fatalf("want call=call-3 in %q", buf.String())
	}
}

func TestClearCallOnlyRemovesMatchingCall(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	// A reused web room: the next call registered before the old one cleared.
	SetCall("t1/web-d", "old")
	SetCall("t1/web-d", "new")
	ClearCall("t1/web-d", "old")
	defer ClearCall("t1/web-d", "new")

	log.Info("ring", "room", "t1/web-d")
	if !strings.Contains(buf.String(), "call=new") {
		t.Fatalf("want call=new in %q", buf.String())
	}
}

func TestClearCallRemoves(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	SetCall("t1/pstn-e", "call-5")
	ClearCall("t1/pstn-e", "call-5")

	log.Info("pstn: room ended", "room", "t1/pstn-e")
	if strings.Contains(buf.String(), "call=") {
		t.Fatalf("unexpected call= after clear in %q", buf.String())
	}
}
