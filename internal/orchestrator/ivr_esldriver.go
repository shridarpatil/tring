package orchestrator

import (
	"context"
	"time"

	"github.com/percipia/eslgo"

	"github.com/whatomate/tring/internal/ivr"
)

// eslDriver is the FreeSWITCH implementation of ivr.CallDriver: it adapts the
// executor's existing ESL node handlers (runGreeting/runMenu/runTransfer/…) to
// the shared walker. The handlers set the per-call step scratch
// (stepAction/Detail/DTMF) and return the outcome; result() packages that into
// the ivr.NodeResult that ivr.Walk records. One eslDriver per inbound call,
// bound to that call's ESL connection + state.
type eslDriver struct {
	e    *Executor
	conn *eslgo.Conn
	st   *callState
}

var _ ivr.CallDriver = (*eslDriver)(nil)

// clear resets the per-node step scratch before a handler runs.
func (d *eslDriver) clear() { d.st.stepAction, d.st.stepDetail, d.st.stepDTMF = "", "", "" }

// result packages the scratch the handler set into a NodeResult.
func (d *eslDriver) result(outcome string) ivr.NodeResult {
	return ivr.NodeResult{Outcome: outcome, Action: d.st.stepAction, Detail: d.st.stepDetail, DTMF: d.st.stepDTMF}
}

func (d *eslDriver) Greeting(ctx context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	d.clear()
	o, err := d.e.runGreeting(ctx, d.conn, n, d.st)
	return d.result(o), err
}

func (d *eslDriver) Menu(ctx context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	d.clear()
	o, err := d.e.runMenu(ctx, d.conn, n, d.st)
	return d.result(o), err
}

func (d *eslDriver) Transfer(ctx context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	d.clear()
	o, err := d.e.runTransfer(ctx, d.conn, n, d.st)
	return d.result(o), err
}

func (d *eslDriver) HTTPCallback(ctx context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	d.clear()
	o, err := d.e.runHTTPCallback(ctx, d.conn, n, d.st)
	return d.result(o), err
}

func (d *eslDriver) Hangup(ctx context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	d.clear()
	o, err := d.e.runHangup(ctx, d.conn, n, d.st)
	return d.result(o), err
}

// Gather is still a scaffold on the FreeSWITCH side (matches the prior TODO):
// it follows the default outcome. (The pion driver implements real gather.)
func (d *eslDriver) Gather(_ context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	d.clear()
	d.st.log.Warn("ivr node not yet implemented, following default outcome", "type", n.Type, "node", n.ID, "outcome", "default")
	return ivr.NodeResult{Outcome: "default"}, nil
}

// Timing evaluates the node's schedule against the deployment clock.
func (d *eslDriver) Timing(_ context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	d.clear()
	outcome := evalTimingOutcome(n.Config, time.Now())
	d.st.stepAction = "branch"
	d.st.log.Info("ivr timing", "node", n.ID, "outcome", outcome)
	return d.result(outcome), nil
}

// GotoFlow is not yet implemented (matches the prior TODO stub): end the call.
func (d *eslDriver) GotoFlow(_ context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	d.clear()
	d.st.log.Warn("ivr node not yet implemented, ending call", "type", n.Type, "node", n.ID)
	return ivr.NodeResult{}, nil
}
