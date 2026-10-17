package api

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
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
