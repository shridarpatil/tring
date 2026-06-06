package freeswitch

import (
	"strings"
	"testing"
)

// parseJobUUID and buildVarBlock are tested here as pure unit tests so the
// reply-parsing contract is locked down even when no FreeSWITCH is reachable
// (CI without the integration job, dev box, etc.). The integration test in
// integration_test.go exercises the wire path.

func TestParseJobUUIDStandardReply(t *testing.T) {
	in := "+OK Job-UUID: 7a873c14-4fac-4957-97cd-567916b72d8b"
	got := parseJobUUID(in)
	want := "7a873c14-4fac-4957-97cd-567916b72d8b"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseJobUUIDTolerantOfTrailingWhitespace(t *testing.T) {
	cases := []string{
		"+OK Job-UUID: abc-123\n",
		"+OK Job-UUID:  abc-123  ",
		"+OK Job-UUID: abc-123\r\n",
	}
	for _, in := range cases {
		if got := parseJobUUID(in); got != "abc-123" {
			t.Errorf("parseJobUUID(%q) = %q, want \"abc-123\"", in, got)
		}
	}
}

func TestParseJobUUIDMissingReturnsEmpty(t *testing.T) {
	for _, in := range []string{"+OK", "-ERR something failed", ""} {
		if got := parseJobUUID(in); got != "" {
			t.Errorf("parseJobUUID(%q) = %q, want empty", in, got)
		}
	}
}

func TestBuildVarBlockEmpty(t *testing.T) {
	if got := buildVarBlock(nil); got != "" {
		t.Errorf("nil vars got %q, want empty (callers concatenate)", got)
	}
	if got := buildVarBlock(map[string]string{}); got != "" {
		t.Errorf("empty vars got %q, want empty", got)
	}
}

func TestBuildVarBlockWrapsInCurlies(t *testing.T) {
	got := buildVarBlock(map[string]string{"k": "v"})
	if got != "{k=v}" {
		t.Errorf("got %q, want {k=v}", got)
	}
}

func TestBuildVarBlockMultipleVars(t *testing.T) {
	// Map iteration order is unstable, so we assert structure not ordering.
	got := buildVarBlock(map[string]string{
		"continue_on_fail":    "GATEWAY_DOWN",
		"hangup_after_bridge": "true",
	})
	if !strings.HasPrefix(got, "{") || !strings.HasSuffix(got, "}") {
		t.Errorf("not wrapped: %q", got)
	}
	if !strings.Contains(got, "continue_on_fail=GATEWAY_DOWN") {
		t.Errorf("missing continue_on_fail entry: %q", got)
	}
	if !strings.Contains(got, "hangup_after_bridge=true") {
		t.Errorf("missing hangup_after_bridge entry: %q", got)
	}
	if !strings.Contains(got, ",") {
		t.Errorf("multiple vars should be comma-separated: %q", got)
	}
}
