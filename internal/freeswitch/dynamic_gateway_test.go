package freeswitch

import (
	"strings"
	"testing"
)

func TestRenderGatewayFromDomain(t *testing.T) {
	out, err := renderGateway(Gateway{Name: "t_x", ProxyIP: "198.51.100.20", FromDomain: "192.0.2.10"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `<param name="from-domain" value="192.0.2.10"></param>`; !strings.Contains(string(out), want) {
		t.Errorf("missing %s in:\n%s", want, out)
	}
}

func TestRenderGatewayOmitsEmptyFromDomain(t *testing.T) {
	out, err := renderGateway(Gateway{Name: "t_x", ProxyIP: "198.51.100.20"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "from-domain") {
		t.Errorf("unexpected from-domain in:\n%s", out)
	}
}

// IMS-style register trunk: the identity registered (username) differs from
// the digest user (auth-username), and the realm is the carrier's SIP domain,
// so REGISTER and calls go to the SBC via proxy.
func TestRenderGatewayRegisterWithProxyAndAuthUsername(t *testing.T) {
	out, err := renderGateway(Gateway{
		Name: "t_x", Register: true,
		Username: "+919800000001", AuthUsername: "+919800000001@ims.example.com",
		Password: "secret", Realm: "ims.example.com", ProxyIP: "198.51.100.20",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<param name="username" value="+919800000001"></param>`,
		`<param name="auth-username" value="+919800000001@ims.example.com"></param>`,
		`<param name="realm" value="ims.example.com"></param>`,
		`<param name="proxy" value="198.51.100.20"></param>`,
		`<param name="register" value="true"></param>`,
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
}

// A plain register trunk (aggregator-style) renders exactly as before: no
// proxy, no auth-username — the realm is resolvable and is the registrar.
func TestRenderGatewayRegisterWithoutProxyUnchanged(t *testing.T) {
	out, err := renderGateway(Gateway{Name: "t_x", Register: true, Username: "u", Password: "p", Realm: "sip.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{`name="proxy"`, `name="auth-username"`} {
		if strings.Contains(string(out), unwanted) {
			t.Errorf("unexpected %s in:\n%s", unwanted, out)
		}
	}
}
