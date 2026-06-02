package freeswitch

import "testing"

func TestParseRegistrations(t *testing.T) {
	body := []byte(`{"row_count":2,"rows":[
		{"reg_user":"alice","realm":"t_default.tring.local","url":"sofia/internal/..."},
		{"reg_user":"Bob","realm":"T_Default.Tring.Local","url":"sofia/internal/..."}
	]}`)
	got := parseRegistrations(body)

	if !got[RegKey("alice", "t_default.tring.local")] {
		t.Error("alice should be registered")
	}
	// Case-insensitive match.
	if !got[RegKey("bob", "t_default.tring.local")] {
		t.Error("bob should match case-insensitively")
	}
	if got[RegKey("carol", "t_default.tring.local")] {
		t.Error("carol was never registered")
	}
}

// Zero registrations: FreeSWITCH omits "rows" — must not panic, yields empty.
func TestParseRegistrationsEmpty(t *testing.T) {
	for _, body := range []string{`{"row_count":0}`, `-ERR no reply`, ``} {
		if got := parseRegistrations([]byte(body)); len(got) != 0 {
			t.Errorf("body %q → %d entries, want 0", body, len(got))
		}
	}
}
