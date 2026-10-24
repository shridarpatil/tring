// Package ivrdrive is the IVR as a pion participant: an ivr.CallDriver that runs
// a flow against a room with no FreeSWITCH. Prompts are played as the IVR's own
// audio track (a prompt.Player injected via SFU.InjectSource); the caller's DTMF
// arrives over /rtc signaling and is fed to DTMF(). Transfer routes the call to
// a human via the Agents seam — a room membership change (the agent joins, the
// IVR participant leaves) implemented by the signaling layer.
//
// The graph traversal is ivr.Walk — the SAME walker the FreeSWITCH executor
// uses — so the phone IVR and the web IVR can't diverge.
package ivrdrive

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shridarpatil/tring/internal/ivr"
)

// Prompts plays a prompt audio file into the room, blocking until it finishes
// (or ctx is done). The real implementation streams an Ogg/Opus file through a
// prompt.Player; tests fake it. Node prompt text is pre-rendered to an audio
// file at flow-save time (see the API's flow handler), so the play path is
// uniform — text and uploaded audio both arrive here as a file.
//
// Stop halts the prompt currently playing (if any), flushing queued audio so a
// caller who presses a valid option mid-prompt (barge-in) is taken to the next
// step immediately instead of after the prompt finishes. It's a no-op when
// nothing is playing and leaves the player usable for the next Play (unlike a
// permanent Close).
type Prompts interface {
	Play(ctx context.Context, file string) error
	Stop()
}

// Target identifies who a Transfer node routes to.
type Target struct {
	Team     string // ring a team (with its strategy/queue) — takes precedence
	AgentSIP string // ring one specific agent
	Label    string // what the agent's incoming-call UI shows (never an internal id); the node's `label` overrides the caller ref
	Answered func() // optional: called once an agent answers, before the call is waited out
}

// Agents rings an agent into the call's room — the transfer membership change.
// It blocks until an agent joins the room (outcome "completed") or nobody
// answers in time / there are no agents ("no_answer"). The IVR participant
// leaves once an agent is admitted; from the caller's side the IVR voice is
// simply replaced by the agent. The signaling layer implements this against
// agent presence; tests fake it. A nil Agents means transfer reports no_answer.
// An agent answered but the caller then hung up is ("completed", ErrCallEnded).
type Agents interface {
	Ring(ctx context.Context, room string, t Target) (outcome string, err error)
}

// ErrCallEnded is returned by Agents.Ring when an agent answered and the call
// then ended: the transfer is recorded with its outcome, and the flow stops.
var ErrCallEnded = errors.New("ivrdrive: call ended")

// Dialer places an outbound phone call to number and, once it answers, joins it
// into the call's room (the transfer-to-number counterpart of Agents.Ring). It
// calls answered (stop the ringing audio) once the callee picks up, then blocks
// until that call ends: the callee hanging up with the caller still on the line
// is "completed"; the caller hanging up is ("completed", ErrCallEnded). A ring
// that fails — busy, no answer within ring, invalid number, no trunk — is
// "no_answer". The signaling
// layer implements it on the PSTN originator; tests fake it. A nil Dialer means
// a number transfer reports no_answer. callerID is the number to present ("" =
// the trunk's own pick); the Dialer must refuse one the tenant doesn't hold.
type Dialer interface {
	Dial(ctx context.Context, room, number, callerID string, ring time.Duration, answered func()) (outcome string, err error)
}

// NodeEvent is a node whose webhook should be sent: it finished with Outcome,
// or (a transfer) it was "answered". Vars is a copy of the flow vars.
type NodeEvent struct {
	Node    *ivr.Node
	Outcome string
	DTMF    string
	Vars    map[string]string
}

// defaultRingSeconds bounds a transfer-to-number ring when the node sets none.
const defaultRingSeconds = 30

// Now exposes the driver's clock so ivr.Walk stamps each step's wall-clock
// execution time into calls.ivr_path (per-step timing in the call's full log).
// Runtime-only: the simulator has no such method, so its traces stay untimed.
func (d *Driver) Now() time.Time { return d.now() }

// Driver implements ivr.CallDriver against the pion media plane.
type Driver struct {
	prompts   Prompts
	agents    Agents
	dialer    Dialer
	dialAudio func() string     // audio file to loop while a number rings ("" = silence)
	room      string            // the call's room id (where a transfer admits the agent)
	clientRef string            // caller label shown to the agent on transfer
	dtmf      chan string       // caller DTMF digits (fed by DTMF())
	now       func() time.Time  // clock for Timing (injectable in tests)
	http      *http.Client      // http_callback client
	vars      map[string]string // flow vars gathered along the way
	onNode    func(NodeEvent)   // sends a node's webhook (nil = off)
	log       *slog.Logger

	played []string // prompt files played, in order (trace/test visibility)
}

// Config builds a Driver.
type Config struct {
	Prompts   Prompts
	Agents    Agents           // rings agents on Transfer; nil → transfer reports no_answer
	Dialer    Dialer           // dials a number on Transfer; nil → number transfer reports no_answer
	DialAudio func() string    // file looped to the caller while a number rings (tenant hold music); nil/"" → silence
	Room      string           // the call's room id (transfer admits the agent here)
	ClientRef string           // caller label shown to the agent (empty → "Web Caller")
	Now       func() time.Time // nil → time.Now
	HTTP      *http.Client     // http_callback client; nil → SSRF-guarded default (safehttp)
	Logger    *slog.Logger
	// Vars seeds the flow vars before the walk — {{caller}} (the caller's
	// number / client ref) and {{did}} (the number they dialed) — so an
	// http_callback can tell the backend who is calling which number.
	Vars map[string]string
	// OnNodeEvent is called for a node that names a webhook (config
	// "webhook_id") when it finishes — a transfer when it's answered or not
	// answered. It must not block the walk.
	OnNodeEvent func(NodeEvent)
}

// New builds a pion IVR driver. The DTMF inbox is buffered so a digit that
// arrives just before a menu starts waiting isn't lost.
func New(cfg Config) *Driver {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	vars := make(map[string]string, len(cfg.Vars))
	for k, v := range cfg.Vars {
		vars[k] = v
	}
	return &Driver{
		prompts:   cfg.Prompts,
		agents:    cfg.Agents,
		dialer:    cfg.Dialer,
		dialAudio: cfg.DialAudio,
		room:      cfg.Room,
		clientRef: cfg.ClientRef,
		dtmf:      make(chan string, 4),
		now:       cfg.Now,
		http:      cfg.HTTP,
		vars:      vars,
		onNode:    cfg.OnNodeEvent,
		log:       cfg.Logger.With("component", "ivrdrive"),
	}
}

// DTMF delivers a caller digit (from /rtc signaling) to a waiting menu/gather.
// Non-blocking: if nothing is collecting, the digit is dropped.
func (d *Driver) DTMF(digit string) {
	select {
	case d.dtmf <- digit:
	default:
	}
}

// StepDone (ivr.Walk's step hook) sends a finished node's webhook. An answered
// transfer already sent its own when the agent or number picked up.
func (d *Driver) StepDone(_ context.Context, n *ivr.Node, st ivr.Step) {
	if n.Type == ivr.NodeTransfer && st.Outcome == "completed" {
		return
	}
	d.nodeEvent(n, st.Outcome, st.DTMFInput)
}

// nodeEvent sends n's webhook, if it names one.
func (d *Driver) nodeEvent(n *ivr.Node, outcome, dtmf string) {
	if d.onNode == nil || ivr.StringFromConfig(n.Config, "webhook_id") == "" {
		return
	}
	vars := make(map[string]string, len(d.vars))
	for k, v := range d.vars {
		vars[k] = v
	}
	d.onNode(NodeEvent{Node: n, Outcome: outcome, DTMF: dtmf, Vars: vars})
}

// Played returns the prompt files played, in order (test/trace visibility).
func (d *Driver) Played() []string { return append([]string(nil), d.played...) }

// Vars returns the flow variables gathered so far.
func (d *Driver) Vars() map[string]string { return d.vars }

// play plays a node's audio_file prompt (no-op when absent) and returns the file
// played (for the trace Detail). Node prompt text is pre-rendered to an audio
// file at flow-save time, so by call time a text node carries an audio_file too.
func (d *Driver) play(ctx context.Context, n *ivr.Node) (string, error) {
	file := ivr.StringFromConfig(n.Config, "audio_file")
	if file == "" {
		return "", nil
	}
	d.played = append(d.played, file)
	return file, d.prompts.Play(ctx, file)
}

// drainDTMF clears any buffered digits so a menu doesn't consume a stale one.
func (d *Driver) drainDTMF() {
	for {
		select {
		case <-d.dtmf:
		default:
			return
		}
	}
}

// awaitDigit waits up to timeout for a caller digit. ok=false on timeout.
func (d *Driver) awaitDigit(ctx context.Context, timeout time.Duration) (string, bool) {
	select {
	case digit := <-d.dtmf:
		return digit, true
	case <-time.After(timeout):
		return "", false
	case <-ctx.Done():
		return "", false
	}
}

// playAndCollect plays a node's prompt while listening for a caller digit so the
// caller can barge in (interrupt the prompt) instead of waiting it out. The
// prompt streams in a goroutine; `accept` decides which digits barge in while it
// plays — a menu passes "is this a configured option" so a stray/invalid key is
// ignored and the prompt keeps playing; a gather accepts any digit. The first
// accepted digit stops the prompt (Stop flushes queued audio) and returns
// immediately. If the prompt finishes with no accepted digit, it then waits up
// to timeout for ANY digit (so an invalid choice after the prompt can drive a
// retry). ok=false on timeout/cancellation; file is the prompt played (for the
// trace Detail), err is a real play failure (a missing file), never our own stop.
func (d *Driver) playAndCollect(ctx context.Context, n *ivr.Node, timeout time.Duration, accept func(string) bool) (digit string, ok bool, file string, err error) {
	file = ivr.StringFromConfig(n.Config, "audio_file")
	if file != "" {
		d.played = append(d.played, file)
	}

	playDone := make(chan struct{})
	var playErr error
	go func() {
		defer close(playDone)
		if file != "" {
			playErr = d.prompts.Play(ctx, file)
		}
	}()

	digit, ok = d.collect(ctx, playDone, timeout, accept)
	d.prompts.Stop() // flush any queued audio so a barge-in halts the prompt now
	<-playDone       // current prompt fully stopped before we return (no overlap on replay)

	if playErr != nil && !errors.Is(playErr, context.Canceled) {
		return "", false, file, playErr
	}
	return digit, ok, file, nil
}

// collect waits for a digit across two phases. While the prompt plays (phase 1)
// only an accepted digit barges in; non-accepted digits are dropped so the prompt
// keeps playing. Once the prompt finishes (phase 2) it waits up to timeout for
// ANY digit. ok=false on timeout or ctx cancellation.
func (d *Driver) collect(ctx context.Context, playDone <-chan struct{}, timeout time.Duration, accept func(string) bool) (string, bool) {
	for { // phase 1: prompt is playing — barge in only on an accepted digit
		select {
		case digit := <-d.dtmf:
			if accept(digit) {
				return digit, true
			}
			// not accepted mid-prompt → ignore, let the prompt keep playing
		case <-playDone:
			// phase 2: prompt finished — accept any digit, up to timeout
			return d.awaitDigit(ctx, timeout)
		case <-ctx.Done():
			return "", false
		}
	}
}

// Greeting plays the greeting and continues. An interruptible greeting stops at
// the caller's first keypress (that key is consumed, not passed to a next menu).
func (d *Driver) Greeting(ctx context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	if !ivr.BoolFromConfig(n.Config, "interruptible") {
		file, err := d.play(ctx, n)
		if err != nil {
			return ivr.NodeResult{}, err
		}
		return ivr.NodeResult{Outcome: "default", Action: "play_audio", Detail: file}, nil
	}

	file := ivr.StringFromConfig(n.Config, "audio_file")
	if file == "" {
		return ivr.NodeResult{Outcome: "default", Action: "play_audio"}, nil
	}
	d.played = append(d.played, file)
	d.drainDTMF() // a key pressed before the greeting began doesn't skip it

	playDone := make(chan struct{})
	var playErr error
	go func() {
		defer close(playDone)
		playErr = d.prompts.Play(ctx, file)
	}()
	select {
	case digit := <-d.dtmf:
		d.prompts.Stop()
		<-playDone
		return ivr.NodeResult{Outcome: "default", Action: "play_audio", Detail: file, DTMF: digit}, nil
	case <-playDone:
	}
	if playErr != nil {
		return ivr.NodeResult{}, playErr
	}
	return ivr.NodeResult{Outcome: "default", Action: "play_audio", Detail: file}, nil
}

// Menu plays the prompt and collects one digit, retrying on an invalid choice.
// Outcomes mirror the FreeSWITCH executor: "digit:<n>" on a valid option,
// "timeout" when no digit arrives, "max_retries" when the caller keeps pressing
// invalid digits.
func (d *Driver) Menu(ctx context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	options := ivr.MapFromConfig(n.Config, "options")
	timeout := time.Duration(ivr.IntFromConfig(n.Config, "timeout_seconds", 10)) * time.Second
	retries := ivr.IntFromConfig(n.Config, "max_retries", 3)

	// Only a configured option barges in while the prompt plays; an invalid key
	// is ignored mid-prompt and only counts as a bad choice (→ retry) after it.
	isOption := func(digit string) bool { _, ok := options[digit]; return ok }

	var file string
	for attempt := 0; attempt <= retries; attempt++ {
		d.drainDTMF()
		digit, ok, f, err := d.playAndCollect(ctx, n, timeout, isOption)
		if err != nil {
			return ivr.NodeResult{}, err
		}
		file = f
		if !ok {
			return ivr.NodeResult{Outcome: "timeout", Action: "prompt_dtmf", Detail: file}, nil
		}
		if _, valid := options[digit]; valid {
			return ivr.NodeResult{Outcome: "digit:" + digit, Action: "prompt_dtmf", Detail: file, DTMF: digit}, nil
		}
		// invalid → retry (replay prompt)
	}
	return ivr.NodeResult{Outcome: "max_retries", Action: "prompt_dtmf", Detail: file}, nil
}

// Gather plays the prompt and collects up to max_digits digits (what the
// editor writes; num_digits is the older API name, default 10 as in the
// simulator), ending early on the terminator (default '#') or timeout. Fewer
// than min_digits (default 1) takes the timeout branch; otherwise the
// collected digits are stored in vars[store_as].
func (d *Driver) Gather(ctx context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	ndigits := ivr.IntFromConfig(n.Config, "max_digits", ivr.IntFromConfig(n.Config, "num_digits", 10))
	minDigits := ivr.IntFromConfig(n.Config, "min_digits", 1)
	terminator := ivr.StringFromConfig(n.Config, "terminator")
	if terminator == "" {
		terminator = "#"
	}
	timeout := time.Duration(ivr.IntFromConfig(n.Config, "timeout_seconds", 10)) * time.Second
	store := ivr.StringFromConfig(n.Config, "store_as")

	d.drainDTMF()
	// A gather collects free-form digits, so any keypress barges in on the prompt.
	digit, ok, file, err := d.playAndCollect(ctx, n, timeout, func(string) bool { return true })
	if err != nil {
		return ivr.NodeResult{}, err
	}
	var collected strings.Builder
	for {
		if !ok {
			break // input ended by timeout
		}
		if digit == terminator {
			break
		}
		collected.WriteString(digit)
		if collected.Len() >= ndigits {
			break
		}
		digit, ok = d.awaitDigit(ctx, timeout)
	}
	if collected.Len() == 0 || collected.Len() < minDigits {
		return ivr.NodeResult{Outcome: "timeout", Action: "gather", Detail: file, DTMF: collected.String()}, nil
	}
	if store != "" {
		d.vars[store] = collected.String()
	}
	return ivr.NodeResult{Outcome: "default", Action: "gather", Detail: file, DTMF: collected.String()}, nil
}

// Timing branches on the node schedule against the driver's clock.
func (d *Driver) Timing(_ context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	return ivr.NodeResult{Outcome: ivr.EvalTimingOutcome(n.Config, d.now()), Action: "branch"}, nil
}

// Transfer routes the call to an agent (or team) by ringing them into the room
// — the membership change. When an agent joins, the IVR participant leaves and
// the caller is connected; the outcome is "completed". If nobody answers (or no
// Agents/agents), it's "no_answer" so the flow can branch. A node with a
// `number` dials that phone number instead (see transferToNumber). All targets
// accept {{vars}} — e.g. a number or team returned by an earlier http_callback.
// The node's optional prompt (e.g. "this call may be recorded") plays first.
func (d *Driver) Transfer(ctx context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	// A missing announcement must not cost the caller their agent: log and ring.
	if file, err := d.play(ctx, n); err != nil && ctx.Err() == nil {
		d.log.Warn("ivrdrive transfer: announcement failed, transferring anyway", "file", file, "err", err)
	}
	if _, isNumber := n.Config["number"]; isNumber {
		return d.transferToNumber(ctx, n, ivr.SubstituteVars(ivr.StringFromConfig(n.Config, "number"), d.vars))
	}
	label := d.clientRef
	if l := ivr.SubstituteVars(ivr.StringFromConfig(n.Config, "label"), d.vars); l != "" {
		label = l // a masked label: the agent never sees the caller's real number
	}
	if label == "" {
		label = "Web Caller"
	}
	target := Target{
		Team:     ivr.SubstituteVars(ivr.StringFromConfig(n.Config, "team"), d.vars),
		AgentSIP: ivr.SubstituteVars(ivr.StringFromConfig(n.Config, "agent_sip"), d.vars),
		Label:    label,
		Answered: func() { d.nodeEvent(n, "answered", "") },
	}
	detail := target.Team
	if detail == "" {
		detail = target.AgentSIP
	}
	if detail == "" {
		detail = "all agents"
	}
	if d.agents == nil {
		d.log.Warn("ivrdrive transfer: no agent ring configured, reporting no_answer")
		return ivr.NodeResult{Outcome: "no_answer", Action: "transfer", Detail: detail}, nil
	}
	outcome, err := d.agents.Ring(ctx, d.room, target)
	if errors.Is(err, ErrCallEnded) {
		return ivr.NodeResult{Outcome: outcome, Action: "transfer", Detail: detail, End: true}, nil
	}
	if err != nil {
		return ivr.NodeResult{Action: "transfer", Detail: detail}, err
	}
	return ivr.NodeResult{Outcome: outcome, Action: "transfer", Detail: detail}, nil
}

// GotoFlow is not yet implemented (matches the executor's TODO stub): end.
func (d *Driver) GotoFlow(_ context.Context, _ *ivr.Node) (ivr.NodeResult, error) {
	return ivr.NodeResult{}, nil
}

// Hangup plays an optional goodbye and ends the call (terminal).
func (d *Driver) Hangup(ctx context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	file, _ := d.play(ctx, n)
	return ivr.NodeResult{Action: "hangup", Detail: file}, nil
}

// HTTPCallback calls the integrator's backend (via the shared, transport-
// agnostic ivr.RunHTTPCallback) so the flow can route on data only they have,
// stashing top-level 2xx JSON fields into flow vars. Branches http:2xx /
// http:non2xx; never errors the call.
func (d *Driver) HTTPCallback(ctx context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	timeout := time.Duration(ivr.IntFromConfig(n.Config, "timeout_seconds", 10)) * time.Second
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	outcome, stash, _ := ivr.RunHTTPCallback(rctx, n, d.vars, d.http)
	for k, v := range stash {
		d.vars[k] = v // pion plane: flow vars only (no FS channel vars)
	}
	detail := strings.ToUpper(ivr.StringFromConfig(n.Config, "method")) + " " + ivr.SubstituteVars(ivr.StringFromConfig(n.Config, "url"), d.vars)
	return ivr.NodeResult{Outcome: outcome, Action: "http_call", Detail: detail}, nil
}

// transferToNumber dials number via the Dialer, ringing up to ring_seconds and
// presenting caller_id when set. A blank number, or a number/caller_id still
// holding an unresolved {{var}} (the var was never set), is not dialed — it
// reports no_answer so the flow's fallback runs.
func (d *Driver) transferToNumber(ctx context.Context, n *ivr.Node, number string) (ivr.NodeResult, error) {
	callerID := ivr.SubstituteVars(ivr.StringFromConfig(n.Config, "caller_id"), d.vars)
	if d.dialer == nil || number == "" || strings.Contains(number, "{{") || strings.Contains(callerID, "{{") {
		d.log.Warn("ivrdrive transfer: number not dialable, reporting no_answer",
			"number", number, "caller_id", callerID, "dialer", d.dialer != nil)
		return ivr.NodeResult{Outcome: "no_answer", Action: "transfer", Detail: number}, nil
	}
	ring := time.Duration(ivr.IntFromConfig(n.Config, "ring_seconds", defaultRingSeconds)) * time.Second
	stopAudio := d.loopDialAudio(ctx)
	answered := func() {
		stopAudio()
		d.nodeEvent(n, "answered", "")
	}
	outcome, err := d.dialer.Dial(ctx, d.room, number, callerID, ring, answered)
	stopAudio() // no answer: the ring is over
	if errors.Is(err, ErrCallEnded) {
		return ivr.NodeResult{Outcome: outcome, Action: "transfer", Detail: number, End: true}, nil
	}
	if err != nil {
		return ivr.NodeResult{Action: "transfer", Detail: number}, err
	}
	return ivr.NodeResult{Outcome: outcome, Action: "transfer", Detail: number}, nil
}

// loopDialAudio plays the dial audio (the tenant's hold music) on repeat through
// the IVR's own voice until the returned stop func runs — so the caller isn't
// left in silence while the number rings. It rides the IVR player rather than a
// separate source because a phone caller's port hears one publisher at a time,
// and during the walk that's the IVR. No-op when no audio is configured. The
// stop func is idempotent (the dialer stops it on answer, the driver after).
func (d *Driver) loopDialAudio(ctx context.Context) func() {
	file := ""
	if d.dialAudio != nil {
		file = d.dialAudio()
	}
	if file == "" || d.prompts == nil {
		return func() {}
	}
	actx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for actx.Err() == nil {
			if err := d.prompts.Play(actx, file); err != nil {
				if actx.Err() == nil {
					d.log.Warn("ivrdrive transfer: dial audio failed", "file", file, "err", err)
				}
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			d.prompts.Stop() // flush the clip already queued on the player
			<-done
		})
	}
}
