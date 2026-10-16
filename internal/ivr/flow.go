// Package ivr holds the transport-agnostic IVR flow model and the shared graph
// walker. It has NO FreeSWITCH/ESL, pion, or DB dependency — the per-node
// effects live behind the CallDriver interface, implemented by the orchestrator
// (FreeSWITCH) and the media gateway (pion). The flow data model, parsing, and
// pure helpers are shared by the live executor, the pion driver, and the
// deterministic simulator so the three can't diverge. See docs/src/content/docs/architecture/overview.md.
package ivr

import (
	"encoding/json"
	"fmt"
)

// Node type constants — the node kinds the flow editor emits.
const (
	NodeStart        = "start"
	NodeGreeting     = "greeting"
	NodeMenu         = "menu"
	NodeGather       = "gather"
	NodeHTTPCallback = "http_callback"
	NodeTransfer     = "transfer"
	NodeGotoFlow     = "goto_flow"
	NodeTiming       = "timing"
	NodeHangup       = "hangup"
)

// NodePosition is the editor canvas position (UI-only).
type NodePosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Node is one flow-graph node.
type Node struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Label    string                 `json:"label"`
	Position NodePosition           `json:"position"`
	Config   map[string]interface{} `json:"config"`
}

// Edge connects two nodes; Condition is the source handle id (= a node outcome).
type Edge struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Condition string `json:"condition"`
}

// Flow is a parsed flow definition (the JSONB in flows.definition).
type Flow struct {
	Version   int    `json:"version"`
	Nodes     []Node `json:"nodes"`
	Edges     []Edge `json:"edges"`
	EntryNode string `json:"entry_node"`
}

// Parse unmarshals a flow definition and enforces the one invariant the walker
// relies on: a non-empty entry node. Empty input → (nil, nil).
func Parse(defJSON []byte) (*Flow, error) {
	if len(defJSON) == 0 {
		return nil, nil
	}
	var flow Flow
	if err := json.Unmarshal(defJSON, &flow); err != nil {
		return nil, fmt.Errorf("bad flow JSON: %w", err)
	}
	if flow.EntryNode == "" {
		return nil, fmt.Errorf("flow has no entry node")
	}
	return &flow, nil
}

// FindNode returns the node with the given id.
func FindNode(flow *Flow, id string) (*Node, bool) {
	for i := range flow.Nodes {
		if flow.Nodes[i].ID == id {
			return &flow.Nodes[i], true
		}
	}
	return nil, false
}

// FindNextNode returns the destination of the edge leaving fromID on the given
// condition (outcome), or "" if none.
func FindNextNode(flow *Flow, fromID, condition string) string {
	for _, e := range flow.Edges {
		if e.From == fromID && e.Condition == condition {
			return e.To
		}
	}
	return ""
}
