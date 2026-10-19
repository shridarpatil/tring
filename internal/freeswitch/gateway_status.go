package freeswitch

// gateway_status.go reads each SIP gateway's live health from FreeSWITCH
// so the admin console can show an online/offline badge per trunk. One
// ESL call per UI refresh — cheap (~10ms) and good enough for the
// current trunk counts.
//
// It reads `sofia xmlstatus gateway`, NOT `sofia status`. The plain-text
// form only exposes the *registration* state, which says nothing about
// whether an IP-auth peer is reachable: such a gateway sends no REGISTER
// and therefore sits at NOREG forever, alive or dead. The XML form also
// carries sofia's OPTIONS-ping verdict (<status>UP|DOWN</status> plus the
// measured <pingtime>), which is the only field that reflects the peer
// actually answering. Reading the state alone reported dead trunks as
// "online".

import (
	"context"
	"encoding/xml"
	"io"
	"strconv"
	"strings"

	"github.com/percipia/eslgo"
	"github.com/percipia/eslgo/command"
)

// GatewayHealth is one gateway's live state as FreeSWITCH sees it. The
// zero value means "FreeSWITCH has no such gateway".
type GatewayHealth struct {
	State   string  // registration state: REGED, NOREG, FAIL_WAIT, …
	PingUp  bool    // sofia's OPTIONS-ping verdict (<status>UP</status>)
	PingRTT float64 // <pingtime> in milliseconds; >0 only once a ping was actually answered

	// Call counters since the gateway was (re)loaded — UptimeSec ago. A
	// reachable trunk can still fail every call (carrier provisioning), so
	// these are what tell "calls work" apart from "pings work". CallsIn is
	// only attributed when sofia can tie an inbound call to the gateway
	// (registered trunks); IP-auth inbound calls aren't counted here.
	CallsIn, CallsOut, FailedIn, FailedOut int
	UptimeSec                              float64
}

// GatewayHealths returns gatewayName → live health. Use Status to
// translate one into the online/offline label surfaced in the UI.
func (c *Client) GatewayHealths(ctx context.Context) (map[string]GatewayHealth, error) {
	conn, err := eslgo.Dial(c.addr, c.password, func() {})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	resp, err := conn.SendCommand(ctx, command.API{
		Command:   "sofia",
		Arguments: "xmlstatus gateway",
	})
	if err != nil {
		return nil, err
	}
	return parseGatewayHealth(resp.Body), nil
}

// parseGatewayHealth reads the <gateways><gateway>… document emitted by
// `sofia xmlstatus gateway`. Fail-soft: a malformed or "-ERR" body yields
// an empty map, so a FreeSWITCH hiccup shows trunks as not_loaded rather
// than erroring the list — the same discipline as parseRegistrations.
func parseGatewayHealth(body []byte) map[string]GatewayHealth {
	out := map[string]GatewayHealth{}
	var doc struct {
		Gateways []struct {
			Name      string  `xml:"name"`
			State     string  `xml:"state"`
			Status    string  `xml:"status"`
			PingTime  float64 `xml:"pingtime"`
			Uptime    int64   `xml:"uptime-usec"`
			CallsIn   int     `xml:"calls-in"`
			CallsOut  int     `xml:"calls-out"`
			FailedIn  int     `xml:"failed-calls-in"`
			FailedOut int     `xml:"failed-calls-out"`
		} `xml:"gateway"`
	}
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	// FreeSWITCH declares ISO-8859-1; the fields we read are ASCII, so
	// pass the bytes through rather than pulling in a charset package.
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	if err := dec.Decode(&doc); err != nil {
		return out
	}
	for _, g := range doc.Gateways {
		if g.Name == "" {
			continue
		}
		out[g.Name] = GatewayHealth{
			State:     strings.TrimSpace(g.State),
			PingUp:    strings.EqualFold(strings.TrimSpace(g.Status), "UP"),
			PingRTT:   g.PingTime,
			CallsIn:   g.CallsIn,
			CallsOut:  g.CallsOut,
			FailedIn:  g.FailedIn,
			FailedOut: g.FailedOut,
			UptimeSec: float64(g.Uptime) / 1e6,
		}
	}
	return out
}

// Detail is the raw FreeSWITCH view of the gateway, shown in the
// console's status tooltip. It pairs the reg-state with the ping verdict
// so an operator can tell "carrier rejected our REGISTER" apart from
// "peer never answered OPTIONS" — the state alone can't say which.
// Empty when FreeSWITCH has no such gateway.
func (h GatewayHealth) Detail() string {
	if h.State == "" {
		return ""
	}
	if !h.PingUp {
		return h.State + " ping=DOWN"
	}
	return h.State + " ping=UP " + strconv.FormatFloat(h.PingRTT, 'f', 2, 64) + "ms"
}

// Status maps a gateway's health to one of the labels the console
// renders: online | offline | connecting | not_loaded | unknown.
//
// Which field carries the truth depends on the auth mode:
//
//   - register=true — the registration state is authoritative. REGED
//     means the carrier accepted us; anything else means it didn't.
//
//   - register=false (IP-auth) — no REGISTER is ever sent, so the state
//     is permanently NOREG and carries no information. Reachability comes
//     from sofia's OPTIONS ping. A gateway starts UP before the first
//     ping completes, so "online" requires a measured round-trip; UP with
//     no reply yet is still settling.
//
// A peer that never answers OPTIONS therefore reads offline. That is
// deliberate: unverified is reported as down, not up.
func Status(h GatewayHealth, register bool) string {
	if h.State == "" {
		return "not_loaded"
	}
	if !register {
		switch {
		case h.PingUp && h.PingRTT > 0:
			return "online"
		case h.PingUp:
			return "connecting"
		default:
			return "offline"
		}
	}
	switch h.State {
	case "REGED":
		return "online"
	case "TRYING", "REGISTER":
		return "connecting"
	case "NOREG", "FAILED", "FAIL_WAIT", "REG_FAIL", "EXPIRED", "NOAUTH",
		"UNREGED", "UNREGISTER":
		return "offline"
	}
	return "unknown"
}
