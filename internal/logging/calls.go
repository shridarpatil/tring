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

// participants maps a participant id to who it is (role, plus sip for agents
// or user for supervisors), added to every log line naming that participant.
// Signaling registers each party as it joins; the SFU clears it on leave.
var participants sync.Map // participant id → []slog.Attr

// SetCall tags log lines for room with call from now on.
func SetCall(room, call string) { calls.Store(room, call) }

// ClearCall stops tagging room, unless it has since been registered to a
// different call (a reused web room), which keeps its own tag.
func ClearCall(room, call string) { calls.CompareAndDelete(room, call) }

// SetParticipant tags log lines for participant with attrs from now on.
func SetParticipant(participant string, attrs ...slog.Attr) { participants.Store(participant, attrs) }

// ClearParticipant stops tagging participant.
func ClearParticipant(participant string) { participants.Delete(participant) }

// WithCallIDs wraps h so that lines naming a registered room also get "call",
// and lines naming a registered participant also get its tags.
func WithCallIDs(h slog.Handler) slog.Handler { return callHandler{h: h} }

// callHandler adds "call" for a registered "room", and the registered tags for
// a "participant", each taken from the record or the logger's With attrs.
// A key the line (or its With attrs) already has is never added again.
type callHandler struct {
	h           slog.Handler
	room        string          // from With attrs
	participant string          // from With attrs
	has         map[string]bool // keys set by With attrs
}

func (c callHandler) Enabled(ctx context.Context, l slog.Level) bool { return c.h.Enabled(ctx, l) }

func (c callHandler) Handle(ctx context.Context, r slog.Record) error {
	room, participant := c.room, c.participant
	has := map[string]bool{}
	for k := range c.has {
		has[k] = true
	}
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "room":
			room = a.Value.Resolve().String()
		case "participant":
			participant = a.Value.Resolve().String()
		}
		has[a.Key] = true
		return true
	})
	var add []slog.Attr
	if room != "" && !has["call"] {
		if id, ok := calls.Load(room); ok {
			add = append(add, slog.String("call", id.(string)))
		}
	}
	if participant != "" {
		if tags, ok := participants.Load(participant); ok {
			for _, a := range tags.([]slog.Attr) {
				if !has[a.Key] {
					add = append(add, a)
				}
			}
		}
	}
	if len(add) > 0 {
		r = r.Clone()
		r.AddAttrs(add...)
	}
	return c.h.Handle(ctx, r)
}

func (c callHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := callHandler{h: c.h.WithAttrs(attrs), room: c.room, participant: c.participant, has: map[string]bool{}}
	for k := range c.has {
		next.has[k] = true
	}
	for _, a := range attrs {
		switch a.Key {
		case "room":
			next.room = a.Value.Resolve().String()
		case "participant":
			next.participant = a.Value.Resolve().String()
		}
		next.has[a.Key] = true
	}
	return next
}

// WithGroup passes through: attrs inside a group aren't top-level room/call.
func (c callHandler) WithGroup(name string) slog.Handler {
	return callHandler{h: c.h.WithGroup(name), room: c.room, participant: c.participant, has: c.has}
}
