// Package mask hides client phone numbers from console users the tenant has
// chosen to mask (tenants.mask_numbers_*). The api masks REST and dashboard
// stream payloads and the gateway masks /rtc ring messages, so both import
// this one definition of who is masked and what a masked number looks like.
package mask

import "strings"

// dot replaces each hidden digit.
const dot = '•'

// Settings are a tenant's per-role masking switches.
type Settings struct {
	Agents      bool `json:"agents"`
	Managers    bool `json:"managers"`
	Supervisors bool `json:"supervisors"`
}

// Any reports whether any role is masked, letting callers skip the per-user
// lookup when the tenant masks nobody.
func (s Settings) Any() bool { return s.Agents || s.Managers || s.Supervisors }

// For reports whether a user with role (admin | supervisor | agent), managing
// at least one team when manager is set, sees masked numbers. Admins never do.
func (s Settings) For(role string, manager bool) bool {
	switch role {
	case "supervisor":
		return s.Supervisors
	case "agent":
		if manager {
			return s.Managers
		}
		return s.Agents
	}
	return false
}

// minDigits is the fewest digits a phone number has. Shorter values (an
// extension, a CRM's numeric id) aren't client numbers and stay readable.
const minDigits = 7

// Number masks a phone number, keeping its first 2 and last 4 digits and any
// leading + and separators. Anything else is returned unchanged: text (a name,
// a SIP URI, a date inside a label) and values under minDigits digits.
func Number(s string) string {
	body := strings.TrimPrefix(s, "+")
	digits := 0
	for _, r := range body {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case strings.ContainsRune(" -().", r):
		default:
			return s
		}
	}
	if digits < minDigits {
		return s
	}
	var b strings.Builder
	b.WriteString(s[:len(s)-len(body)])
	i := 0
	for _, r := range body {
		if r < '0' || r > '9' {
			b.WriteRune(r)
			continue
		}
		if i < 2 || i >= digits-4 {
			b.WriteRune(r)
		} else {
			b.WriteRune(dot)
		}
		i++
	}
	return b.String()
}

// CallNumbers masks the client side of a call: the caller on an inbound call,
// the callee on an outbound one. The tenant's own DID or caller ID stays
// visible. An unknown direction masks both. Like Number, only a value that is
// itself a number is masked: text is never searched for numbers inside it.
func CallNumbers(direction, from, to string) (string, string) {
	if direction != "outbound" {
		from = Number(from)
	}
	if direction != "inbound" {
		to = Number(to)
	}
	return from, to
}
