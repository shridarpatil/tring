package provider

import (
	"fmt"

	"github.com/whatomate/tring/internal/dial"
)

// Loopback is the dev provider — it dials back into FreeSWITCH's own
// dialplan instead of a trunk. Use it to test the originate pipeline
// without a real SIP carrier. Pair it with the stock echo extension (9196)
// or a custom test extension in the dialplan.
type Loopback struct{}

func (Loopback) Name() string              { return "loopback" }
func (Loopback) FreeSWITCHGateway() string { return "" }

func (Loopback) DialString(dest string) string {
	return fmt.Sprintf("loopback/%s", dest)
}

func (Loopback) NormalizeE164(raw string) (string, error) {
	// Internal extensions (9196, 5000-series IVR, ...) aren't E.164.
	// Pass through so dev tests can dial them. Production paths use a
	// real provider with strict normalization.
	return dial.Passthrough(raw)
}
