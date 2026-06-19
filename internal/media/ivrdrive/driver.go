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
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/whatomate/tring/internal/ivr"
)

// Prompts plays a prompt audio file into the room, blocking until it finishes
// (or ctx is done). The real implementation streams an Ogg/Opus file through a
// prompt.Player; tests fake it. Node prompt text is pre-rendered to an audio
// file at flow-save time (see the API's flow handler), so the play path is
// uniform — text and uploaded audio both arrive here as a file.
type Prompts interface {
	Play(ctx context.Context, file string) error
}

// Target identifies who a Transfer node routes to.
type Target struct {
	Team     string // ring a team (with its strategy/queue) — takes precedence
	AgentSIP string // ring one specific agent
	Label    string // what the agent's incoming-call UI shows (never an internal id)
}

// Agents rings an agent into the call's room — the transfer membership change.
// It blocks until an agent joins the room (outcome "completed") or nobody
// answers in time / there are no agents ("no_answer"). The IVR participant
// leaves once an agent is admitted; from the caller's side the IVR voice is
// simply replaced by the agent. The signaling layer implements this against
// agent presence; tests fake it. A nil Agents means transfer reports no_answer.
type Agents interface {
	Ring(ctx context.Context, room string, t Target) (outcome string, err error)
}

// Driver implements ivr.CallDriver against the pion media plane.
type Driver struct {
	prompts   Prompts
	agents    Agents
	room      string            // the call's room id (where a transfer admits the agent)
	clientRef string            // caller label shown to the agent on transfer
	dtmf      chan string       // caller DTMF digits (fed by DTMF())
	now       func() time.Time  // clock for Timing (injectable in tests)
	http      *http.Client      // http_callback client
	vars      map[string]string // flow vars gathered along the way
	log       *slog.Logger

	played []string // prompt files played, in order (trace/test visibility)
}

// Config builds a Driver.
type Config struct {
	Prompts   Prompts
	Agents    Agents           // rings agents on Transfer; nil → transfer reports no_answer
	Room      string           // the call's room id (transfer admits the agent here)
	ClientRef string           // caller label shown to the agent (empty → "Web Caller")
	Now       func() time.Time // nil → time.Now
	HTTP      *http.Client     // nil → http.DefaultClient
	Logger    *slog.Logger
}

// New builds a pion IVR driver. The DTMF inbox is buffered so a digit that
// arrives just before a menu starts waiting isn't lost.
func New(cfg Config) *Driver {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HTTP == nil {
		cfg.HTTP = http.DefaultClient
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Driver{
		prompts:   cfg.Prompts,
		agents:    cfg.Agents,
		room:      cfg.Room,
		clientRef: cfg.ClientRef,
		dtmf:      make(chan string, 4),
		now:       cfg.Now,
		http:      cfg.HTTP,
		vars:      map[string]string{},
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

// Greeting plays the greeting and continues.
func (d *Driver) Greeting(ctx context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	file, err := d.play(ctx, n)
	if err != nil {
		return ivr.NodeResult{}, err
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

	var file string
	for attempt := 0; attempt <= retries; attempt++ {
		d.drainDTMF()
		f, err := d.play(ctx, n)
		if err != nil {
			return ivr.NodeResult{}, err
		}
		file = f
		digit, ok := d.awaitDigit(ctx, timeout)
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

// Gather plays the prompt and collects up to num_digits digits, ending early on
// a terminator ('#') or timeout. Stores the collected digits in vars[store_as].
func (d *Driver) Gather(ctx context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	ndigits := ivr.IntFromConfig(n.Config, "num_digits", 1)
	timeout := time.Duration(ivr.IntFromConfig(n.Config, "timeout_seconds", 10)) * time.Second
	store := ivr.StringFromConfig(n.Config, "store_as")

	d.drainDTMF()
	file, err := d.play(ctx, n)
	if err != nil {
		return ivr.NodeResult{}, err
	}
	var collected strings.Builder
	for collected.Len() < ndigits {
		digit, ok := d.awaitDigit(ctx, timeout)
		if !ok {
			if collected.Len() == 0 {
				return ivr.NodeResult{Outcome: "timeout", Action: "gather", Detail: file}, nil
			}
			break // partial input, ended by timeout
		}
		if digit == "#" {
			break
		}
		collected.WriteString(digit)
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
// Agents/agents), it's "no_answer" so the flow can branch.
func (d *Driver) Transfer(ctx context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	label := d.clientRef
	if label == "" {
		label = "Web Caller"
	}
	target := Target{
		Team:     ivr.StringFromConfig(n.Config, "team"),
		AgentSIP: ivr.StringFromConfig(n.Config, "agent_sip"),
		Label:    label,
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
