// Package dial normalizes phone numbers to E.164 with country-specific
// rules. Lives outside provider/ because providers carry FreeSWITCH wiring
// (gateway XML, dial strings) — number rules are pure.
package dial

import (
	"errors"
	"regexp"
	"strings"
)

var (
	ErrEmpty     = errors.New("number is empty")
	ErrNotIndian = errors.New("not a valid Indian mobile number")
	ErrBadFormat = errors.New("bad number format")
)

// indianMobile matches a 10-digit number starting with 6, 7, 8, or 9 —
// the current TRAI mobile range. Landlines (11-digit with STD code) are
// not yet supported by Tring outbound.
var indianMobile = regexp.MustCompile(`^[6-9]\d{9}$`)

// stripper removes everything except digits and a leading +. Whitespace,
// dashes, parens, dots etc. are noise users routinely paste.
var stripper = regexp.MustCompile(`[^\d+]`)

// India normalizes a user-supplied number to canonical Indian E.164
// (`+91XXXXXXXXXX`). Accepts +91-prefixed, 91-prefixed, 0-prefixed
// (national format), and bare 10-digit forms. Returns ErrNotIndian for
// anything else — including correctly-formatted numbers from other
// countries. That is intentional: this function is only called for the
// Indian trunk providers, which only carry Indian traffic.
func India(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrEmpty
	}
	cleaned := stripper.ReplaceAllString(raw, "")

	// +91 / 91 / 0 / bare-10-digit normalization
	switch {
	case strings.HasPrefix(cleaned, "+91"):
		cleaned = cleaned[3:]
	case strings.HasPrefix(cleaned, "91") && len(cleaned) == 12:
		cleaned = cleaned[2:]
	case strings.HasPrefix(cleaned, "0") && len(cleaned) == 11:
		cleaned = cleaned[1:]
	case strings.HasPrefix(cleaned, "+"):
		// Any other +country code → not Indian.
		return "", ErrNotIndian
	}
	if !indianMobile.MatchString(cleaned) {
		return "", ErrNotIndian
	}
	return "+91" + cleaned, nil
}

// plainNumber is a bare number: an optional leading + and 3-15 digits (the
// E.164 maximum). Nothing else may reach a FreeSWITCH dial string — `|`, `,`,
// `[`, spaces or newlines would add legs, variables or whole ESL commands.
var plainNumber = regexp.MustCompile(`^\+?\d{3,15}$`)

// IsNumber reports whether s is a bare number (see plainNumber). Use it to
// gate stored numbers (DIDs, caller IDs) that end up in dial strings.
func IsNumber(s string) bool { return plainNumber.MatchString(s) }

// Passthrough does no E.164 normalization. Used by the Loopback provider
// (dev, and tenants with no or "generic" trunks) so internal extensions like
// 9196 (echo) still route. Only trims, and requires a bare number.
func Passthrough(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrEmpty
	}
	if !IsNumber(raw) {
		return "", ErrBadFormat
	}
	return raw, nil
}
