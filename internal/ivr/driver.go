package ivr

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// clocked is optionally implemented by a runtime driver so Walk can stamp each
// step with its wall-clock execution time (per-step timing in calls.ivr_path).
// Pure drivers — the simulator, test fakes — don't implement it, keeping their
// traces deterministic and untimed.
type clocked interface{ Now() time.Time }

// stepObserver is optionally implemented by a runtime driver to hear about
// each node once Walk has recorded it (e.g. to send the node's webhook).
type stepObserver interface {
	StepDone(ctx context.Context, n *Node, st Step)
}

// MaxDepth bounds graph traversal so a cyclic (mis-authored) flow can't loop
// forever. Matches the live executor's historical limit.
const MaxDepth = 64

// NodeResult is what a driver returns after running a node: the outcome edge to
// follow ("" = terminal) plus the trace detail Walk records into the Step
// (Action/Detail/DTMF). Carrying these as a return value lets Walk own step
// recording uniformly for every driver — no per-driver recording loop.
type NodeResult struct {
	Outcome string
	Action  string // play_audio, speak, prompt_dtmf, transfer, hangup, branch, http_call…
	Detail  string // file, agent label, URL, …
	DTMF    string // digit consumed at a menu/gather, if any
	End     bool   // record Outcome, but stop here: the call is over (a caller who hung up mid-transfer)
}

// CallDriver is the per-node effect contract: the shared Walk owns graph
// traversal (which node next, by outcome), while a driver performs each node's
// effect against its medium and returns the outcome + trace detail. The same
// flow graph is thus driven identically over FreeSWITCH (orchestrator's
// esldriver) and pion (media gateway's ivrdrive). The `start` node is handled
// by Walk itself (a pure passthrough to "default").
type CallDriver interface {
	Greeting(ctx context.Context, n *Node) (NodeResult, error)
	Menu(ctx context.Context, n *Node) (NodeResult, error)
	Gather(ctx context.Context, n *Node) (NodeResult, error)
	Transfer(ctx context.Context, n *Node) (NodeResult, error)
	HTTPCallback(ctx context.Context, n *Node) (NodeResult, error)
	Timing(ctx context.Context, n *Node) (NodeResult, error)
	GotoFlow(ctx context.Context, n *Node) (NodeResult, error)
	Hangup(ctx context.Context, n *Node) (NodeResult, error) // terminal: Outcome ""
}

// Walk traverses the flow from entry, running each node's effect via the driver
// and following the edge matching the returned outcome, until a terminal node
// ("" outcome), a node with no matching outgoing edge, or MaxDepth. It records
// one Step per node and always returns the (partial) path so a caller can show
// how far the call got and persist the trace. On a driver error the step's
// outcome is "error" and the error is returned.
func Walk(ctx context.Context, flow *Flow, entry string, d CallDriver) ([]Step, error) {
	var path []Step
	clk, timed := d.(clocked)
	obs, observed := d.(stepObserver)
	nodeID := entry
	for depth := 0; depth < MaxDepth; depth++ {
		node, ok := FindNode(flow, nodeID)
		if !ok {
			return path, fmt.Errorf("node %s not found", nodeID)
		}
		var at *time.Time
		if timed {
			t := clk.Now()
			at = &t
		}
		res, err := runNode(ctx, node, d)
		outcome := res.Outcome
		if err != nil {
			outcome = "error"
		}
		path = append(path, Step{
			NodeID: node.ID, NodeType: node.Type,
			Action: res.Action, Detail: res.Detail, DTMFInput: res.DTMF,
			Outcome: outcome, At: at,
		})
		if observed {
			obs.StepDone(ctx, node, path[len(path)-1])
		}
		if err != nil {
			return path, fmt.Errorf("node %s (%s): %w", node.ID, node.Type, err)
		}
		if res.Outcome == "" || res.End {
			return path, nil // terminal node, or the call ended during it
		}
		next := FindNextNode(flow, nodeID, res.Outcome)
		if next == "" {
			return path, nil // no edge for this outcome → end gracefully
		}
		nodeID = next
	}
	return path, errors.New("flow walk exceeded max depth")
}

// runNode dispatches a node to the driver by type. start is a pure passthrough
// (a UI anchor) recorded as a "passthrough" → "default".
func runNode(ctx context.Context, node *Node, d CallDriver) (NodeResult, error) {
	switch node.Type {
	case NodeStart:
		return NodeResult{Outcome: "default", Action: "passthrough"}, nil
	case NodeGreeting:
		return d.Greeting(ctx, node)
	case NodeMenu:
		return d.Menu(ctx, node)
	case NodeGather:
		return d.Gather(ctx, node)
	case NodeTransfer:
		return d.Transfer(ctx, node)
	case NodeHTTPCallback:
		return d.HTTPCallback(ctx, node)
	case NodeTiming:
		return d.Timing(ctx, node)
	case NodeGotoFlow:
		return d.GotoFlow(ctx, node)
	case NodeHangup:
		return d.Hangup(ctx, node)
	default:
		return NodeResult{}, fmt.Errorf("unknown node type %q", node.Type)
	}
}
