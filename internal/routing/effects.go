package routing

import (
	"context"
	"time"
)

// RingOutcome is what a single ring attempt produced.
type RingOutcome struct {
	Answered    bool   // an agent picked up
	Transferred bool   // the caller was blind-transferred away (terminal; no hangup)
	Label       string // attribution detail for the CDR / journey step
}

// Effects is the transport-specific seam the shared Router.Run loop calls: how a
// plane actually rings agents, detects an answer, holds the caller while queued,
// and checks the caller is still on the line. The two media planes differ ONLY
// here — the FreeSWITCH plane implements it with an ESL bridge + CHANNEL_BRIDGE
// attribution and a playback/uuid_break hold; the pion plane with msgIncoming +
// a room-join signal.
//
// The asymmetry the seam absorbs: FreeSWITCH learns of an answer synchronously
// (Ring's bridge returns), while pion learns asynchronously (an agent joins the
// room at any time, including during a hold). Answered() is the backstop the
// Router checks each iteration so a pion answer that lands mid-hold still counts.
type Effects interface {
	// Answered reports an answer observed OUTSIDE a Ring call (the pion room-join
	// backstop). FreeSWITCH returns false (its answer comes back through Ring).
	Answered() (bool, string)
	// CallerAlive reports the caller is still connected (abandon detection).
	CallerAlive(ctx context.Context) bool
	// Ring the ordered candidates and block up to perAttempt for one to answer.
	Ring(ctx context.Context, ordered []string, perAttempt time.Duration) (RingOutcome, error)
	// Hold plays caller hold audio and BLOCKS until ready() returns true, maxDur
	// elapses, or ctx is done. FreeSWITCH plays MOH broken by a side-connection
	// watcher polling ready(); pion waits (optionally playing a hold prompt).
	Hold(ctx context.Context, maxDur time.Duration, ready func() bool)
}
