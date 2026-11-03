package signaling

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// An agent who has been told `joined` is already busy: anything that reacts to
// the join (a transfer or ring started the moment the SDK sees joined) must not
// pick them as a free agent.
func TestAgentOnCallBeforeJoined(t *testing.T) {
	s, url := newClusterNode(t, nil)
	tenantID := uuid.New()
	tenant := tenantID.String()
	openAgent(t, s, tenant, "100") // online, so free() turns on the on-call count alone
	// Away until the backend flag is read, just after ready.
	for deadline := time.Now().Add(2 * time.Second); !s.agentView(context.Background(), tenant).free("100"); {
		if time.Now().After(deadline) {
			t.Fatal("agent never free before joining")
		}
		time.Sleep(5 * time.Millisecond)
	}

	joinRoom(t, url, tenantID, "100", "r1")

	if !s.agentOnCall(tenant, "100") {
		t.Fatal("agentOnCall = false right after joined, want true")
	}
	if s.agentView(context.Background(), tenant).free("100") {
		t.Fatal("agentView free(100) = true right after joined, want false")
	}
}
