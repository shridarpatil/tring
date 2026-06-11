package ivr

// Step is one row in an IVR trace — the path a call took through the graph.
// Reused by the deterministic simulator (Test panel), the live executor
// (persisted to calls.ivr_path), and the pion driver, so a flow's traced path
// and its real path render identically.
type Step struct {
	NodeID    string `json:"node_id"`
	NodeType  string `json:"node_type"`
	Action    string `json:"action"`               // play_audio, speak, prompt_dtmf, transfer, hangup, branch, passthrough, http_call
	Detail    string `json:"detail,omitempty"`     // file path, agent number, URL, etc.
	DTMFInput string `json:"dtmf_input,omitempty"` // digit consumed at a menu/gather, if any
	Outcome   string `json:"outcome"`              // edge handle id taken: "default", "digit:1", "timeout"...
}
