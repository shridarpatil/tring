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
