package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Bus message types.
const (
	MsgRing       = "ring"
	MsgRingCancel = "ring_cancel"
	MsgDecline    = "decline"
)

const busVersion = 1

// ErrNoReceiver is returned by Send when nobody is subscribed to the target
// node's bus: the node crashed (its keys outlive it until TTL) or hasn't
// started its bus yet. The message is lost, so callers treat it as not sent.
var ErrNoReceiver = errors.New("cluster: no receiver on bus")

// Msg is one node-to-node message on the per-node bus. It is internal JSON,
// versioned by V so a rolling upgrade can drop what it doesn't understand.
type Msg struct {
	V      int    `json:"v"`
	Type   string `json:"type"`
	From   string `json:"from_node"` // sender's node id (set by Send)
	Tenant string `json:"tenant"`
	SIP    string `json:"sip"`
	Room   string `json:"room"` // un-namespaced room name, as on the wire to the SDK
	Label  string `json:"label,omitempty"`
	Auto   bool   `json:"auto,omitempty"`
}

func (c *Cluster) busChannel(node string) string { return c.cfg.Prefix + "node:" + node + ":bus" }

// Send publishes m to node's bus, setting V and From. It is a no-op (nil) on a
// nil or closed Cluster. It returns ErrNoReceiver (wrapped) when no subscriber
// got the message, so a ring to a dead node fails fast instead of being waited
// out like a ring nobody answered. A nil error still doesn't mean the message
// was handled: the node may die right after receiving it.
func (c *Cluster) Send(ctx context.Context, node string, m Msg) error {
	if c == nil || c.isClosed() {
		return nil
	}
	m.V, m.From = busVersion, c.cfg.ID
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("cluster: marshal bus message: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, setTimeout)
	defer cancel()
	n, err := c.rdb.Publish(ctx, c.busChannel(node), b).Result()
	if err != nil {
		return fmt.Errorf("cluster: publish to %s: %w", node, err)
	}
	if n == 0 {
		return fmt.Errorf("%w %s", ErrNoReceiver, node)
	}
	return nil
}

// Run subscribes to this node's bus, calling handle for each message in order,
// and heartbeats every Interval with snapshot(). It blocks until ctx ends. A
// message with an unknown version or type is logged and dropped. A heartbeat
// failure is logged at Error once per outage. Run does not delete this node's
// keys on exit; Close does.
func (c *Cluster) Run(ctx context.Context, snapshot func() Snapshot, handle func(Msg)) {
	if c == nil {
		return
	}
	// go-redis PubSub resubscribes on its own after a connection loss.
	sub := c.rdb.Subscribe(ctx, c.busChannel(c.cfg.ID))
	defer sub.Close()

	// One goroutine drains the bus so a slow handler never delays heartbeats;
	// it is the only caller of handle, which keeps delivery in order.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for rm := range sub.Channel() {
			var m Msg
			if err := json.Unmarshal([]byte(rm.Payload), &m); err != nil {
				c.log.Warn("cluster: bad bus message", "err", err)
				continue
			}
			switch {
			case m.V != busVersion:
				c.log.Warn("cluster: dropping bus message with unknown version", "v", m.V, "type", m.Type)
			case m.Type != MsgRing && m.Type != MsgRingCancel && m.Type != MsgDecline:
				c.log.Warn("cluster: dropping bus message with unknown type", "type", m.Type)
			default:
				handle(m)
			}
		}
	}()

	failing := false
	beat := func() {
		err := c.Heartbeat(ctx, snapshot())
		switch {
		case err == nil:
			if failing {
				c.log.Info("cluster: heartbeat recovered")
			}
			failing = false
		case ctx.Err() != nil || errors.Is(err, ErrClosed):
			// Shutting down; not an outage.
		case !failing:
			failing = true
			c.log.Error("cluster: heartbeat failed", "err", err)
		}
	}
	beat()
	t := time.NewTicker(c.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			sub.Close()
			<-done
			return
		case <-t.C:
			beat()
		}
	}
}
