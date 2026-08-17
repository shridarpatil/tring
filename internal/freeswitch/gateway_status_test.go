package freeswitch

import "testing"

// Real `sofia xmlstatus gateway` output, trimmed to the fields we read.
// Note the ISO-8859-1 declaration FreeSWITCH emits — the parser must
// cope with it. t_9dc9595111b3 is an IP-auth trunk whose peer never
// answered an OPTIONS ping (status DOWN, pingtime 0.00); example.com is
// reachable (status UP with a measured round-trip).
const xmlStatusFixture = `<?xml version="1.0" encoding="ISO-8859-1"?>
<gateways>
  <gateway>
    <name>example.com</name>
    <profile>external</profile>
    <state>NOREG</state>
    <status>UP</status>
    <pingtime>0.04</pingtime>
    <pingfreq>25</pingfreq>
  </gateway>
  <gateway>
    <name>t_9dc9595111b3</name>
    <profile>external</profile>
    <state>NOREG</state>
    <status>DOWN</status>
    <pingtime>0.00</pingtime>
    <pingfreq>25</pingfreq>
  </gateway>
  <gateway>
    <name>t_regbased01</name>
    <profile>external</profile>
    <state>REGED</state>
    <status>UP</status>
    <pingtime>0.00</pingtime>
    <pingfreq>25</pingfreq>
  </gateway>
</gateways>
`

func TestParseGatewayHealthReadsPingStatus(t *testing.T) {
	got := parseGatewayHealth([]byte(xmlStatusFixture))
	if len(got) != 3 {
		t.Fatalf("want 3 gateways, got %d (%v)", len(got), got)
	}

	dead, ok := got["t_9dc9595111b3"]
	if !ok {
		t.Fatal("gateway t_9dc9595111b3 missing from parse")
	}
	if dead.State != "NOREG" {
		t.Errorf("state: want NOREG, got %q", dead.State)
	}
	if dead.PingUp {
		t.Error("PingUp: want false for a gateway FreeSWITCH marks DOWN")
	}

	live := got["example.com"]
	if !live.PingUp {
		t.Error("PingUp: want true for a gateway FreeSWITCH marks UP")
	}
	if live.PingRTT <= 0 {
		t.Errorf("PingRTT: want >0 for an answered ping, got %v", live.PingRTT)
	}
}

// The reported bug: an IP-auth trunk whose peer is unreachable was shown
// as "online" because sofia's NOREG reg-state was read as healthy. The
// ping status is the signal that actually reflects reachability.
func TestStatusIPAuthUnreachableIsOffline(t *testing.T) {
	h := GatewayHealth{State: "NOREG", PingUp: false, PingRTT: 0}
	if got := Status(h, false); got != "offline" {
		t.Errorf("IP-auth trunk with ping DOWN: want offline, got %q", got)
	}
}

func TestStatusIPAuthAnsweredPingIsOnline(t *testing.T) {
	h := GatewayHealth{State: "NOREG", PingUp: true, PingRTT: 0.04}
	if got := Status(h, false); got != "online" {
		t.Errorf("IP-auth trunk with answered ping: want online, got %q", got)
	}
}

// FreeSWITCH seeds a new gateway as UP before the first ping completes,
// so "UP with no measured round-trip" means "not established yet", not
// "healthy".
func TestStatusIPAuthUnmeasuredIsConnecting(t *testing.T) {
	h := GatewayHealth{State: "NOREG", PingUp: true, PingRTT: 0}
	if got := Status(h, false); got != "connecting" {
		t.Errorf("IP-auth trunk with no ping reply yet: want connecting, got %q", got)
	}
}

func TestStatusRegisterBased(t *testing.T) {
	cases := []struct {
		state string
		want  string
	}{
		{"REGED", "online"},
		{"TRYING", "connecting"},
		{"REGISTER", "connecting"},
		{"NOREG", "offline"},
		{"FAIL_WAIT", "offline"},
		{"FAILED", "offline"},
		{"EXPIRED", "offline"},
	}
	for _, c := range cases {
		// Ping health must not override a failing registration.
		h := GatewayHealth{State: c.state, PingUp: true, PingRTT: 0.04}
		if got := Status(h, true); got != c.want {
			t.Errorf("register-based %s: want %s, got %s", c.state, c.want, got)
		}
	}
}

// Detail backs the console's status tooltip, so it must name the reason a
// trunk reads offline — the reg-state alone can't explain an IP-auth trunk.
func TestGatewayHealthDetail(t *testing.T) {
	cases := []struct {
		name string
		h    GatewayHealth
		want string
	}{
		{"absent", GatewayHealth{}, ""},
		{"ping answered", GatewayHealth{State: "NOREG", PingUp: true, PingRTT: 0.04}, "NOREG ping=UP 0.04s"},
		{"ping unanswered", GatewayHealth{State: "NOREG", PingUp: false}, "NOREG ping=DOWN"},
		{"registered", GatewayHealth{State: "REGED", PingUp: true, PingRTT: 0.02}, "REGED ping=UP 0.02s"},
	}
	for _, c := range cases {
		if got := c.h.Detail(); got != c.want {
			t.Errorf("%s: want %q, got %q", c.name, c.want, got)
		}
	}
}

// A trunk row with no matching gateway in FreeSWITCH isn't offline —
// FreeSWITCH simply never loaded it.
func TestStatusAbsentGatewayIsNotLoaded(t *testing.T) {
	for _, register := range []bool{true, false} {
		if got := Status(GatewayHealth{}, register); got != "not_loaded" {
			t.Errorf("absent gateway (register=%v): want not_loaded, got %q", register, got)
		}
	}
}
