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

// maskCall masks a call's client side in place: the caller (inbound) or callee
// (outbound) number, and the embedder's client_ref, which on a web call is the
// caller's identity (and the console shows it in place of from_number). A
// client_ref is masked only when it is itself a number, like the numbers.
func maskCall(c *Call) {
	c.FromNumber, c.ToNumber = mask.CallNumbers(c.Direction, c.FromNumber, c.ToNumber)
	if c.ClientRef != nil {
		ref := mask.Number(*c.ClientRef)
		c.ClientRef = &ref
	}
}

// maskCalls masks the client side of calls in place.
func maskCalls(calls []Call) {
	for i := range calls {
		maskCall(&calls[i])
	}
}

// maskFrame masks the client side of one hub SSE frame
// ("event: <type>\ndata: <json>\n\n") the way maskCall masks a call: only
// call.ringing carries numbers today, with the call's direction, plus
// call.transferred's "to" (an agent, a team or an external number). Frames
// without number fields pass unchanged. A frame that can't be parsed returns
// nil: dropped for the masked subscriber rather than risk a full number.
func maskFrame(frame []byte) []byte {
	head, rest, ok := bytes.Cut(frame, []byte("\ndata: "))
	if !ok {
		return nil
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(bytes.TrimSuffix(rest, []byte("\n\n")), &data) != nil {
		return nil
	}
	str := func(k string) (string, bool) {
		var v string
		raw, ok := data[k]
		return v, ok && json.Unmarshal(raw, &v) == nil
	}
	typ := string(bytes.TrimPrefix(head, []byte("event: ")))
	if typ == events.TypeCallTransferred {
		target, ok := str("to")
		if !ok {
			return frame
		}
		data["to"], _ = json.Marshal(mask.Number(target))
		out, err := json.Marshal(data)
		if err != nil {
			return nil
		}
		return events.Frame(typ, out)
	}
	from, hasFrom := str("from_number")
	to, hasTo := str("to_number")
	ref, hasRef := str("client_ref")
	if !hasFrom && !hasTo && !hasRef {
		return frame
	}
	dir, _ := str("direction")
	from, to = mask.CallNumbers(dir, from, to)
	set := func(k, v string) { data[k], _ = json.Marshal(v) }
	if hasFrom {
		set("from_number", from)
	}
	if hasTo {
		set("to_number", to)
	}
	if hasRef {
		set("client_ref", mask.Number(ref))
	}
	out, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	return events.Frame(typ, out)
}
