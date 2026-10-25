package logging

import (
	"context"
	"log/slog"
	"sync"
)

// calls maps a media room id to the id of the call running in it, so every
// log line that names a room also carries its call id ("call=…"), without the
// many call sites that log a room having to know the call. callcdr registers
// a room when it creates the call row and clears it when the call ends.
var calls sync.Map // room id → call id

// SetCall tags log lines for room with call from now on.
func SetCall(room, call string) { calls.Store(room, call) }

// ClearCall stops tagging room, unless it has since been registered to a
// different call (a reused web room), which keeps its own tag.
func ClearCall(room, call string) { calls.CompareAndDelete(room, call) }

// WithCallIDs wraps h so that lines naming a registered room also get "call".
func WithCallIDs(h slog.Handler) slog.Handler { return callHandler{h: h} }

// callHandler adds "call" to any record with a registered "room", taken from
// the record or from the logger's With attrs. A record that already has
// "call" is left alone.
type callHandler struct {
	h       slog.Handler
	room    string // from With attrs
	hasCall bool   // With attrs already set "call"
}

func (c callHandler) Enabled(ctx context.Context, l slog.Level) bool { return c.h.Enabled(ctx, l) }

func (c callHandler) Handle(ctx context.Context, r slog.Record) error {
	room, hasCall := c.room, c.hasCall
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "room":
			room = a.Value.Resolve().String()
		case "call":
			hasCall = true
		}
		return true
	})
	if !hasCall && room != "" {
		if id, ok := calls.Load(room); ok {
			r = r.Clone()
			r.AddAttrs(slog.String("call", id.(string)))
		}
	}
	return c.h.Handle(ctx, r)
}

func (c callHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := callHandler{h: c.h.WithAttrs(attrs), room: c.room, hasCall: c.hasCall}
	for _, a := range attrs {
		switch a.Key {
		case "room":
			next.room = a.Value.Resolve().String()
		case "call":
			next.hasCall = true
		}
	}
	return next
}

// WithGroup passes through: attrs inside a group aren't top-level room/call.
func (c callHandler) WithGroup(name string) slog.Handler {
	return callHandler{h: c.h.WithGroup(name), room: c.room, hasCall: c.hasCall}
}
