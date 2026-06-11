package room

import "time"

// Clock is the room's only source of time, so the grace-window timer can be
// driven deterministically in tests (no sleeping). Production uses RealClock;
// tests use a manual clock that fires pending timers on demand.
type Clock interface {
	// AfterFunc schedules f to run after d, returning a Timer that can cancel it
	// (mirrors time.AfterFunc).
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a cancelable scheduled callback. Stop reports whether it stopped the
// timer before it fired (mirrors *time.Timer.Stop).
type Timer interface {
	Stop() bool
}

// RealClock is the production Clock, backed by the time package.
type RealClock struct{}

// AfterFunc implements Clock using time.AfterFunc. *time.Timer already satisfies
// Timer (its Stop returns bool).
func (RealClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }
