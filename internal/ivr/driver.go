package ivr

import (
	"context"
	"errors"
	"fmt"
)

// MaxDepth bounds graph traversal so a cyclic (mis-authored) flow can't loop
// forever. Matches the live executor's historical limit.
const MaxDepth = 64

// CallDriver is the per-node effect contract: the shared Walk owns graph
// traversal (which node next, by outcome), while a driver performs each node's
// effect against its medium and returns the outcome edge to follow. Returning
// "" ends the call (a terminal node, e.g. hangup). The same flow graph is thus
// driven identically over FreeSWITCH (orchestrator) and pion (media gateway);
// the deterministic simulator is a third implementation.
//
// start is handled by Walk itself (a pure passthrough to "default").
type CallDriver interface {
	Greeting(ctx context.Context, n *Node) (outcome string, err error)
	Menu(ctx context.Context, n *Node) (outcome string, err error)
	Gather(ctx context.Context, n *Node) (outcome string, err error)
	Transfer(ctx context.Context, n *Node) (outcome string, err error)
	HTTPCallback(ctx context.Context, n *Node) (outcome string, err error)
	Timing(ctx context.Context, n *Node) (outcome string, err error)
	GotoFlow(ctx context.Context, n *Node) (outcome string, err error)
	Hangup(ctx context.Context, n *Node) (outcome string, err error) // terminal: returns ""
}

// Walk traverses the flow from entry, running each node's effect via the driver
// and following the edge matching the returned outcome, until a terminal node
// ("" outcome), a node with no matching outgoing edge, or MaxDepth. It returns
// the path taken (NodeID/NodeType/Outcome per step) plus any driver error; the
// partial path is always returned so a caller can show how far the call got.
func Walk(ctx context.Context, flow *Flow, entry string, d CallDriver) ([]Step, error) {
	var path []Step
	nodeID := entry
	for depth := 0; depth < MaxDepth; depth++ {
		node, ok := FindNode(flow, nodeID)
		if !ok {
			return path, fmt.Errorf("node %s not found", nodeID)
		}
		outcome, err := runNode(ctx, node, d)
		path = append(path, Step{NodeID: node.ID, NodeType: node.Type, Outcome: outcome})
		if err != nil {
			return path, fmt.Errorf("node %s (%s): %w", node.ID, node.Type, err)
		}
		if outcome == "" {
			return path, nil // terminal node
		}
		next := FindNextNode(flow, nodeID, outcome)
		if next == "" {
			return path, nil // no edge for this outcome → end gracefully
		}
		nodeID = next
	}
	return path, errors.New("flow walk exceeded max depth")
}

// runNode dispatches a node to the driver by type. start is a pure passthrough.
func runNode(ctx context.Context, node *Node, d CallDriver) (string, error) {
	switch node.Type {
	case NodeStart:
		return "default", nil
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
		return "", fmt.Errorf("unknown node type %q", node.Type)
	}
}
