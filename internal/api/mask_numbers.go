package api

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/mask"
)

// shouldMask reports whether p sees masked client numbers under s. A nil
// Principal is an API key (server to server), which is never masked.
func shouldMask(s mask.Settings, p *Principal) bool {
	return p != nil && s.For(p.Role, len(p.ManagedTeams) > 0)
}

// masksNumbers reads the tenant's masking switches for p. It fails closed:
// when the switches can't be read a non-admin is masked, since the full
// number must never reach a masked user's browser.
func (s *Server) masksNumbers(ctx context.Context, p *Principal) bool {
	if p == nil || p.IsAdmin() {
		return false
	}
	var ms mask.Settings
	if err := s.db.Pool.QueryRow(ctx, s.q.GetTenantMaskNumbers, p.TenantID).
		Scan(&ms.Agents, &ms.Managers, &ms.Supervisors); err != nil {
		s.log.Warn("mask settings read failed; masking", "err", err)
		return true
	}
	return shouldMask(ms, p)
}

// maskCall masks a call's client numbers in place.
func maskCall(c *Call) {
	c.FromNumber = mask.Number(c.FromNumber)
	c.ToNumber = mask.Number(c.ToNumber)
}

// maskCalls masks the client numbers of calls in place.
func maskCalls(calls []Call) {
	for i := range calls {
		maskCall(&calls[i])
	}
}

// numberKeys are the event payload fields carrying a client number (only
// call.ringing has them today).
var numberKeys = []string{"from_number", "to_number"}

// maskFrame masks the number fields of one hub SSE frame
// ("event: <type>\ndata: <json>\n\n"). Frames without them pass unchanged.
func maskFrame(frame []byte) []byte {
	head, rest, ok := bytes.Cut(frame, []byte("\ndata: "))
	if !ok {
		return frame
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(bytes.TrimSuffix(rest, []byte("\n\n")), &data) != nil {
		return frame
	}
	changed := false
	for _, k := range numberKeys {
		var v string
		if raw, ok := data[k]; ok && json.Unmarshal(raw, &v) == nil {
			data[k], _ = json.Marshal(mask.Number(v))
			changed = true
		}
	}
	if !changed {
		return frame
	}
	out, err := json.Marshal(data)
	if err != nil {
		return frame
	}
	return events.Frame(string(bytes.TrimPrefix(head, []byte("event: "))), out)
}
