package signaling

import (
	"encoding/json"
	"sort"
	"testing"
)

// TestWireContractFrozen pins the /rtc JSON field names. The browser SDK mirrors
// these exactly (sdk/js/src/rtc-protocol.ts), and the Phase-2 brain/edge split is
// a SERVER-INTERNAL change that must not alter the client contract. If this
// fails, the public wire contract changed — update the SDK contract deliberately
// (and version it), don't just relax the test. See docs/ARCHITECTURE.md.
func TestWireContractFrozen(t *testing.T) {
	m := msg{
		Type: "x", Room: "r", Name: "n", Token: "t", FlowID: "f", Digit: "1",
		SDP: "s", Kind: "video", Candidate: json.RawMessage(`{}`),
		Participant: "p", State: "st", ICEServers: []ICEServer{{URLs: []string{"u"}}},
		From: "fr", ToTeam: "sales", ToSip: "bob", Reason: "re",
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := []string{
		"type", "room", "name", "session_token", "flow_id", "digit", "sdp",
		"kind", "candidate", "participant_id", "state", "ice_servers", "from",
		"to_team", "to_sip", "reason",
	}
	gotKeys := make([]string, 0, len(got))
	for k := range got {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)
	sort.Strings(want)
	if len(gotKeys) != len(want) {
		t.Fatalf("wire fields = %v, want %v", gotKeys, want)
	}
	for i := range want {
		if gotKeys[i] != want[i] {
			t.Fatalf("wire fields = %v, want %v (contract drift)", gotKeys, want)
		}
	}

	// ICEServer's own field names are part of the contract too.
	ib, _ := json.Marshal(ICEServer{URLs: []string{"u"}, Username: "a", Credential: "c"})
	var iceGot map[string]json.RawMessage
	_ = json.Unmarshal(ib, &iceGot)
	for _, k := range []string{"urls", "username", "credential"} {
		if _, ok := iceGot[k]; !ok {
			t.Errorf("ICEServer missing frozen field %q", k)
		}
	}
}
