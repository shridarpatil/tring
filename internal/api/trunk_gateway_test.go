package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/freeswitch"
)

func TestTrunkToGatewayFromDomain(t *testing.T) {
	g := trunkToGateway(Trunk{ID: uuid.New(), ExtraParams: json.RawMessage(`{"from_domain":"192.0.2.10"}`)})
	if g.FromDomain != "192.0.2.10" {
		t.Errorf("got from_domain=%q", g.FromDomain)
	}
}

func TestTrunkToGatewayIgnoresMissingOrBadExtraParams(t *testing.T) {
	for _, raw := range []string{``, `{}`, `{"from_domain":123}`, `not json`} {
		g := trunkToGateway(Trunk{ID: uuid.New(), ExtraParams: json.RawMessage(raw)})
		if g.FromDomain != "" {
			t.Errorf("%q: got from_domain=%q", raw, g.FromDomain)
		}
	}
}

func TestTrunkToGatewayRegisterProxyAndAuthUsername(t *testing.T) {
	g := trunkToGateway(Trunk{
		ID: uuid.New(), Register: true, SIPUsername: "+919800000001", ProxyIP: "198.51.100.20",
		ExtraParams: json.RawMessage(`{"auth_username":"+919800000001@ims.example.com","from_domain":"ims.example.com"}`),
	})
	if g.AuthUsername != "+919800000001@ims.example.com" || g.ProxyIP != "198.51.100.20" || g.FromDomain != "ims.example.com" {
		t.Errorf("got auth_username=%q proxy=%q from_domain=%q", g.AuthUsername, g.ProxyIP, g.FromDomain)
	}
}

func TestTrunkCalls(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	h := freeswitch.GatewayHealth{State: "NOREG", CallsIn: 3, CallsOut: 5, FailedIn: 1, FailedOut: 5, UptimeSec: 7200}

	ip := trunkCalls(h, false, now)
	if ip.Out != 5 || ip.FailedOut != 5 || !ip.Since.Equal(now.Add(-2*time.Hour)) {
		t.Errorf("ip-auth: got %+v", ip)
	}
	if ip.In != nil || ip.FailedIn != nil {
		t.Error("ip-auth trunks must not report inbound counts (FreeSWITCH doesn't attribute them)")
	}

	reg := trunkCalls(h, true, now)
	if reg.In == nil || *reg.In != 3 || *reg.FailedIn != 1 {
		t.Errorf("register: got in=%v failed_in=%v", reg.In, reg.FailedIn)
	}

	if trunkCalls(freeswitch.GatewayHealth{}, true, now) != nil {
		t.Error("a gateway FreeSWITCH doesn't have must report no counters")
	}
}
