package freeswitch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
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

// Tata's SIP-over-AWS guide registers with proxy + username + password and no
// realm: the realm must fall back to the proxy, never render empty.
func TestRenderGatewayRegisterRealmDefaultsToProxy(t *testing.T) {
	out, err := renderGateway(Gateway{Name: "t_x", Register: true, Username: "0091XXXXXXXXXX", Password: "p", ProxyIP: "198.51.100.20:5061"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<param name="realm" value="198.51.100.20:5061"></param>`,
		`<param name="proxy" value="198.51.100.20:5061"></param>`,
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
	if strings.Contains(string(out), `name="realm" value=""`) {
		t.Errorf("empty realm rendered:\n%s", out)
	}
}

// sofia's rescan won't reload an already-loaded gateway or unload a removed
// one, so WriteGateways must report exactly the gateways that changed or
// went away (to killgw), and leave new/unchanged ones out.
func TestWriteGatewaysReportsChangedAndRemoved(t *testing.T) {
	old := GatewayDir
	GatewayDir = t.TempDir()
	defer func() { GatewayDir = old }()

	idA, idB := uuid.New(), uuid.New()
	a := Gateway{ID: idA.String(), Name: GatewayName(idA), ProxyIP: "198.51.100.20"}
	b := Gateway{ID: idB.String(), Name: GatewayName(idB), Register: true, Username: "u", Password: "p", ProxyIP: "198.51.100.30"}

	stale, err := WriteGateways([]Gateway{a, b})
	if err != nil || len(stale) != 0 {
		t.Fatalf("first write: stale=%v err=%v, want none (both new)", stale, err)
	}
	pathA := filepath.Join(GatewayDir, "t_"+a.ID+".xml")
	before, _ := os.Stat(pathA)

	if stale, err = WriteGateways([]Gateway{a, b}); err != nil || len(stale) != 0 {
		t.Fatalf("rewrite unchanged: stale=%v err=%v, want none", stale, err)
	}
	if after, _ := os.Stat(pathA); !after.ModTime().Equal(before.ModTime()) {
		t.Error("unchanged gateway file was rewritten")
	}

	b.Username = "u2" // edit b
	if stale, err = WriteGateways([]Gateway{a, b}); err != nil || len(stale) != 1 || stale[0] != b.Name {
		t.Fatalf("edit b: stale=%v err=%v, want [%s]", stale, err, b.Name)
	}

	// a deleted/deactivated: its file goes and its gateway must be unloaded
	if stale, err = WriteGateways([]Gateway{b}); err != nil || len(stale) != 1 || stale[0] != a.Name {
		t.Fatalf("remove a: stale=%v err=%v, want [%s]", stale, err, a.Name)
	}
	if _, err := os.Stat(pathA); !os.IsNotExist(err) {
		t.Error("removed gateway's file still present")
	}
}

// FreeSWITCH expands $${global} (and ${var}) inside gateway XML, so a trunk
// field like from-domain=$${tring_fs_basic_auth} would put a secret into the
// SIP headers sent to the tenant's proxy. Such gateways are never rendered;
// the others still are.
func TestWriteGatewaysSkipsVariableExpansion(t *testing.T) {
	old := GatewayDir
	GatewayDir = t.TempDir()
	defer func() { GatewayDir = old }()

	idGood, idBad := uuid.New(), uuid.New()
	good := Gateway{ID: idGood.String(), Name: GatewayName(idGood), ProxyIP: "198.51.100.20"}
	for _, bad := range []Gateway{
		{FromDomain: "$${tring_fs_basic_auth}"},
		{Register: true, Username: "u", Password: "x${tring_esl_password}"},
		{Realm: "${sip_from_host}"},
		{AuthUsername: "a\nb"},
	} {
		bad.ID, bad.Name = idBad.String(), GatewayName(idBad)
		if bad.ProxyIP == "" {
			bad.ProxyIP = "198.51.100.30"
		}
		if _, err := renderGateway(bad); !errors.Is(err, ErrUnsafeValue) {
			t.Errorf("renderGateway(%+v) err = %v, want ErrUnsafeValue", bad, err)
		}
		if _, err := WriteGateways([]Gateway{good, bad}); err != nil {
			t.Fatalf("WriteGateways: %v", err)
		}
		if _, err := os.Stat(filepath.Join(GatewayDir, "t_"+bad.ID+".xml")); !os.IsNotExist(err) {
			t.Errorf("unsafe gateway %+v was written", bad)
		}
		if _, err := os.Stat(filepath.Join(GatewayDir, "t_"+good.ID+".xml")); err != nil {
			t.Errorf("safe gateway not written: %v", err)
		}
	}
	// A literal $ without a brace (e.g. in a password) is fine.
	ok := Gateway{ID: idBad.String(), Name: GatewayName(idBad), Register: true, Username: "u", Password: "pa$$word", ProxyIP: "198.51.100.30"}
	if _, err := renderGateway(ok); err != nil {
		t.Errorf("password with plain $: %v", err)
	}
}

// A trunk that turns unsafe after it was rendered is unloaded, not left
// running on its old config.
func TestWriteGatewaysUnloadsTrunkThatTurnsUnsafe(t *testing.T) {
	old := GatewayDir
	GatewayDir = t.TempDir()
	defer func() { GatewayDir = old }()

	id := uuid.New()
	g := Gateway{ID: id.String(), Name: GatewayName(id), ProxyIP: "198.51.100.20"}
	if _, err := WriteGateways([]Gateway{g}); err != nil {
		t.Fatal(err)
	}
	g.FromDomain = "$${tring_esl_password}"
	stale, err := WriteGateways([]Gateway{g})
	if err != nil || len(stale) != 1 || stale[0] != g.Name {
		t.Fatalf("stale=%v err=%v, want [%s]", stale, err, g.Name)
	}
	if _, err := os.Stat(filepath.Join(GatewayDir, "t_"+g.ID+".xml")); !os.IsNotExist(err) {
		t.Error("unsafe trunk's old file still present")
	}
}
