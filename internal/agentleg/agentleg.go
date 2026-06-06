// Package agentleg builds the single FreeSWITCH bridge dial-string for an
// agent's call leg. It is shared by the API (outbound click-to-call,
// POST /v1/calls) and the orchestrator (inbound IVR transfers) so the leg is
// byte-identical regardless of how the call arrived — the agent side is one
// definition, not two that can drift.
package agentleg

import (
	"fmt"
	"strings"
)

// Options describes one agent leg. Transport mirrors the configured agent
// transport: "gateway" routes the leg to the media gateway as a SIP endpoint
// (ADR-001 — agents speak WebRTC, never register SIP); anything else is the
// legacy direct-SIP transport (user/<sip>@<domain>).
type Options struct {
	Transport string // "gateway" | "sip"
	Gateway   string // FS gateway name for the gateway transport (e.g. "tring-gw")
	SIP       string // agent SIP username
	Domain    string // SIP domain (sip transport only)
	TenantID  string // tenant id, sent to the gateway as the X-Tring-Tenant header
	Label     string // agent-facing caller label; sanitized, "" omits the header

	// Attribute adds tring_agent=<sip> so the orchestrator's CHANNEL_BRIDGE
	// handler can attribute the answer to this agent — needed for IVR transfers
	// and ring-all (where we otherwise can't tell which leg picked up).
	Attribute bool
	// AutoAnswer adds X-Tring-Auto-Answer=1 so the gateway auto-answers the
	// agent's browser — used by backend-initiated click-to-call, not inbound
	// transfers (which ring the agent for a manual accept).
	AutoAnswer bool
}

// Build returns the FreeSWITCH bridge dial-string for the leg.
func Build(o Options) string {
	if o.Transport == "gateway" {
		vars := make([]string, 0, 4)
		if o.Attribute {
			vars = append(vars, "tring_agent="+o.SIP)
		}
		vars = append(vars, "sip_h_X-Tring-Tenant="+o.TenantID)
		if o.AutoAnswer {
			vars = append(vars, "sip_h_X-Tring-Auto-Answer=1")
		}
		if l := sanitizeLabel(o.Label); l != "" {
			vars = append(vars, "sip_h_X-Tring-Agent-Label="+l)
		}
		return fmt.Sprintf("[%s]sofia/gateway/%s/%s", strings.Join(vars, ","), o.Gateway, o.SIP)
	}
	// Legacy direct-SIP transport: the agent registers to FreeSWITCH itself.
	if o.Attribute {
		return fmt.Sprintf("[tring_agent=%s]user/%s@%s", o.SIP, o.SIP, o.Domain)
	}
	return fmt.Sprintf("user/%s@%s", o.SIP, o.Domain)
}

// sanitizeLabel strips characters that would break the FreeSWITCH `[k=v,...]`
// channel-variable block (or inject extra vars) and caps the length, so an
// agent-facing label is safe to embed in the dial string.
func sanitizeLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case ',', '[', ']', '=', '\'', '"':
			return -1
		}
		if r < 0x20 {
			return -1
		}
		return r
	}, s)
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.TrimSpace(s)
}
