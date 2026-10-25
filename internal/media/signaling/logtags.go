package signaling

import (
	"log/slog"

	"github.com/shridarpatil/tring/internal/logging"
	"github.com/shridarpatil/tring/internal/media/room"
)

// tagParticipant names pid in the logs: every line with participant=pid gets
// role= (plus sip= for agents, user= for supervisors) until the SFU drops it
// on leave. Roles: agent, caller, supervisor, callee, ivr, hold, xfermsg.
func tagParticipant(pid room.ParticipantID, role string, extra ...slog.Attr) {
	logging.SetParticipant(string(pid), append([]slog.Attr{slog.String("role", role)}, extra...)...)
}
