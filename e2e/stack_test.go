//go:build e2e

package e2e

import "testing"

// TestStackSeeds seeds a tenant the way a CRM admin would: agents, a team, a
// flow, and an uploaded hold-music tone set as the tenant's hold music.
func TestStackSeeds(t *testing.T) {
	api := requireStack(t)
	a1Sip, team := uniq("seed-a1"), uniq("seed-t1")
	a1 := api.CreateAgent(a1Sip, "Seed Agent 1")
	a2 := api.CreateAgent(uniq("seed-a2"), "Seed Agent 2")
	api.CreateTeam(team)
	api.AddTeamAgent(team, a2)
	flow := api.CreateFlow(uniq("seed-flow"), TransferFlow(map[string]string{"agent_sip": a1Sip}))
	var phase float64
	path := api.UploadAudio(uniq("seed-hold"), WAV(Tone(FreqHold, 8000, 3*8000, &phase), 8000))
	api.SetHoldMusic(path)
	for name, id := range map[string]string{"agent a1": a1, "agent a2": a2, "flow": flow, "hold music path": path} {
		if id == "" {
			t.Errorf("%s: empty id", name)
		}
	}
}
