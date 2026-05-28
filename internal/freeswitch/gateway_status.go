package freeswitch

// gateway_status.go reads `sofia status` from FreeSWITCH and surfaces
// each gateway's state so the admin console can show online/offline
// badges per trunk. One ESL call per UI refresh — cheap (~10ms) and
// good enough for the current trunk counts.

import (
	"context"
	"strings"

	"github.com/percipia/eslgo"
	"github.com/percipia/eslgo/command"
)

// GatewayStatuses returns gatewayName → FreeSWITCH state string (REGED,
// NOREG, FAILED, etc.). Use StatusFromState to translate to the
// online/connecting/offline labels surfaced in the UI.
func (c *Client) GatewayStatuses(ctx context.Context) (map[string]string, error) {
	conn, err := eslgo.Dial(c.addr, c.password, func() {})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	resp, err := conn.SendCommand(ctx, command.API{
		Command:   "sofia",
		Arguments: "status",
	})
	if err != nil {
		return nil, err
	}
	return parseGatewayStates(string(resp.Body)), nil
}

// parseGatewayStates scans sofia status output for lines like:
//
//	external::t_b15a9089853e\tgateway\tsip:FreeSWITCH@127.0.0.1:5070\tNOREG
//	external::t_22fdd1492b76\tgateway\tsip:u@nope.example.com\tFAIL_WAIT (retry: 18s)
//
// The columns are tab-separated; the state column may have a parenthetical
// suffix (retry timer for failed registrations). We split on tabs, take the
// state cell, then return only the leading state token.
func parseGatewayStates(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		if !strings.Contains(line, "::") {
			continue
		}
		cells := strings.Split(line, "\t")
		if len(cells) < 4 {
			continue
		}
		if strings.TrimSpace(cells[1]) != "gateway" {
			continue
		}
		name := strings.TrimSpace(cells[0])
		name = strings.TrimPrefix(name, "external::")
		// State cell: "NOREG" or "FAIL_WAIT (retry: 18s)" — take the
		// first whitespace-delimited token.
		stateCell := strings.TrimSpace(cells[len(cells)-1])
		state := stateCell
		if i := strings.IndexAny(stateCell, " \t"); i > 0 {
			state = stateCell[:i]
		}
		out[name] = state
	}
	return out
}

// StatusFromState maps a FreeSWITCH gateway state to one of four UI
// labels. The semantics differ for register-based vs IP-auth trunks:
// IP-auth never reaches REGED (no REGISTER sent) so NOREG is the
// healthy steady state, while for register-based trunks NOREG means
// "registration is failing — offline."
func StatusFromState(state string, register bool) string {
	switch state {
	case "REGED":
		return "online"
	case "NOREG":
		if register {
			return "offline"
		}
		return "online"
	case "TRYING", "REGISTER":
		return "connecting"
	case "FAILED", "FAIL_WAIT", "REG_FAIL", "EXPIRED", "NOAUTH",
		"UNREGED", "UNREGISTER":
		return "offline"
	case "":
		return "not_loaded"
	}
	return "unknown"
}
