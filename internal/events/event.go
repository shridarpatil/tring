// Package events is the dashboard live-update bus. The gateway publishes typed
// domain events to a per-tenant Redis pub/sub channel; the api subscribes via a
// Hub and fans them to connected SSE dashboards. The wire envelope + type
// constants live here so both binaries share one definition (same discipline as
// internal/queuekeys) and the format can't drift.
package events

import (
	"context"
	"encoding/json"
)

// Event types. Keep in sync with the frontend dispatcher (useDashboardStream.ts).
const (
	TypeCallRinging     = "call.ringing"
	TypeCallRouting     = "call.routing"
	TypeCallAnswered    = "call.answered"
	TypeCallEnded       = "call.ended"
	TypeAgentPresence   = "agent.presence"
	TypeAgentStatus     = "agent.status"
	TypeQueueDepth      = "queue.depth"
	TypeCallSupervised  = "call.supervised"
	TypeCallTransferred = "call.transferred"
	TypeQueueStint      = "queue.stint" // a caller entered (queued_at set) or left (null) a team queue

	// Derived frame types produced by the api Hub (never published to Redis).
	TypeStats    = "stats"
	TypeQueues   = "queues"
	TypeSnapshot = "snapshot"
)

const channelPrefix = "tring:events:"

// Channel is the per-tenant Redis pub/sub channel name.
func Channel(tenantID string) string { return channelPrefix + tenantID }

// Event is the wire envelope. Data is type-specific JSON.
type Event struct {
	Type     string          `json:"type"`
	TenantID string          `json:"tenant_id"`
	TS       int64           `json:"ts"`
	Data     json.RawMessage `json:"data"`
}

// Sink is the publish surface consumers depend on, so they can be tested with a
// fake. A nil *Publisher is a valid no-op Sink.
type Sink interface {
	Publish(ctx context.Context, tenantID, typ string, data any)
}

// Frame renders one SSE frame: "event: <type>\ndata: <json>\n\n".
func Frame(eventType string, data []byte) []byte {
	b := make([]byte, 0, len(data)+len(eventType)+16)
	b = append(b, "event: "...)
	b = append(b, eventType...)
	b = append(b, '\n')
	b = append(b, "data: "...)
	b = append(b, data...)
	b = append(b, '\n', '\n')
	return b
}

// encode builds the JSON-marshaled envelope. Pure (ts injected) so it tests
// without a clock or Redis.
func encode(tenantID, typ string, data any, ts int64) ([]byte, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Event{Type: typ, TenantID: tenantID, TS: ts, Data: raw})
}
