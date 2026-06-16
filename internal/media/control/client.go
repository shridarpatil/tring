package control

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// ErrNotConnected is returned by Send when the control channel is down.
var ErrNotConnected = errors.New("control channel not connected")

// Client is the EDGE side of the control channel: it dials the brain, sends
// EVENTS up, and delivers received COMMANDS to onCommand. It reconnects with
// backoff so a brain restart doesn't take the edge down (media keeps flowing;
// only control is briefly unavailable).
type Client struct {
	url       string
	secret    string
	log       *slog.Logger
	onCommand func(Message)

	mu   sync.Mutex
	conn net.Conn
}

// NewClient builds the edge-side control client. url is the brain's control
// WebSocket (ws:// or wss://). onCommand handles commands from the brain.
func NewClient(url, secret string, onCommand func(Message), log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	if onCommand == nil {
		onCommand = func(Message) {}
	}
	return &Client{
		url:       url,
		secret:    secret,
		log:       log.With("component", "control-client"),
		onCommand: onCommand,
	}
}

// Run dials and services the channel until ctx is cancelled, reconnecting with
// backoff on any drop. Blocking; run it in a goroutine.
func (c *Client) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		if err := c.dialAndServe(ctx); err != nil {
			c.log.Warn("control channel down, retrying", "err", err, "in", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) dialAndServe(ctx context.Context) error {
	d := ws.Dialer{
		Header: ws.HandshakeHeaderHTTP(http.Header{"Authorization": {"Bearer " + c.secret}}),
	}
	conn, _, _, err := d.Dial(ctx, c.url)
	if err != nil {
		return err
	}
	c.setConn(conn)
	c.log.Info("control channel connected", "url", c.url)
	defer func() {
		c.setConn(nil)
		_ = conn.Close()
	}()
	for {
		data, err := wsutil.ReadServerText(conn)
		if err != nil {
			return err
		}
		var m Message
		if err := json.Unmarshal(data, &m); err != nil {
			c.log.Warn("control: bad command json", "err", err)
			continue
		}
		c.onCommand(m)
	}
}

// Send delivers an event to the brain. Returns an error if the channel is down
// (events are best-effort; the caller decides whether to care).
func (c *Client) Send(m Message) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return ErrNotConnected
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return wsutil.WriteClientText(conn, b)
}

// Connected reports whether the channel is currently up.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

func (c *Client) setConn(conn net.Conn) {
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
}
