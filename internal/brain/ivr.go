// Package brain owns the transport-agnostic call lifecycle. The IVR Runner walks
// a flow on a room and guarantees the journey is stamped before teardown — the
// ordering that keeps an abandoned call's path (incl. its transfer step) from
// being lost. Plugs supply the transport via an ivr.CallDriver; brain imports no
// pion/SIP/signaling code.
package brain

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/whatomate/tring/internal/ivr"
	"github.com/whatomate/tring/internal/media/room"
)

// Journey receives the walked path once, before the call settles.
type Journey func(steps []ivr.Step)

type session struct {
	cancel context.CancelFunc
	done   chan struct{} // closed when the walk finished and path is set
	path   []ivr.Step    // read only after <-done
	driver ivr.CallDriver
	onJrn  Journey
	onDone func()
}

// Runner manages one IVR session per room.
type Runner struct {
	log *slog.Logger
	mu  sync.Mutex
	s   map[room.RoomID]*session
}

func NewRunner(log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{log: log.With("component", "brain.ivr"), s: map[room.RoomID]*session{}}
}

// Start claims the room slot, runs setup (the winner's media wiring, e.g.
// InjectSource), then walks the flow in a goroutine. Returns true if this call
// took ownership of the room's IVR slot, false if the slot was already taken or
// setup returned an error. When Start returns false the caller is responsible for
// cleaning up any resources it allocated before calling (setup was NOT called for
// a false-because-already-running return; it was called and rolled back for a
// false-because-setup-failed return). onJourney is called once with the path,
// onDone after teardown — both via Stop, so callers see one consistent ordering.
// setup may be nil (no media wiring needed).
func (r *Runner) Start(ctx context.Context, roomID room.RoomID, flow *ivr.Flow, driver ivr.CallDriver, setup func() error, onJourney Journey, onDone func()) bool {
	cctx, cancel := context.WithCancel(ctx)
	sess := &session{cancel: cancel, done: make(chan struct{}), driver: driver, onJrn: onJourney, onDone: onDone}

	// Claim the slot BEFORE running setup so a concurrent caller sees it taken.
	r.mu.Lock()
	if r.s[roomID] != nil {
		r.mu.Unlock()
		cancel()
		return false // already running — caller must clean up its resources
	}
	r.s[roomID] = sess
	r.mu.Unlock()

	// Run setup outside the lock. On failure roll back the claim.
	if setup != nil {
		if err := setup(); err != nil {
			r.log.Error("ivr: setup failed; rolling back room claim", "room", roomID, "err", err)
			r.mu.Lock()
			delete(r.s, roomID)
			r.mu.Unlock()
			cancel()
			return false
		}
	}

	go func() {
		path, err := ivr.Walk(cctx, flow, flow.EntryNode, driver)
		r.log.Info("ivr flow ended", "room", roomID, "steps", len(path), "err", err)
		sess.path = path
		close(sess.done) // publish path (happens-before Stop's read)
		r.Stop(roomID)
	}()
	return true
}

// Running reports whether an IVR session is active for the room.
func (r *Runner) Running(roomID room.RoomID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.s[roomID] != nil
}

// DTMF routes a digit to the room's driver if it accepts DTMF.
func (r *Runner) DTMF(roomID room.RoomID, digit string) {
	r.mu.Lock()
	sess := r.s[roomID]
	r.mu.Unlock()
	if sess == nil || digit == "" {
		return
	}
	if d, ok := sess.driver.(interface{ DTMF(string) }); ok {
		d.DTMF(digit)
	}
}

// Stop cancels the walk, waits for the path, stamps the journey once, then runs
// onDone. Idempotent: the map-delete under lock picks a single winner. On timeout
// the journey is skipped (and logged), but teardown (onDone) still runs.
func (r *Runner) Stop(roomID room.RoomID) {
	r.mu.Lock()
	sess := r.s[roomID]
	delete(r.s, roomID)
	r.mu.Unlock()
	if sess == nil {
		return
	}
	sess.cancel()
	select {
	case <-sess.done:
		if sess.onJrn != nil {
			sess.onJrn(sess.path)
		}
	case <-time.After(2 * time.Second):
		r.log.Warn("ivr walk did not unwind in time; skipping journey", "room", roomID)
	}
	if sess.onDone != nil {
		sess.onDone()
	}
}
