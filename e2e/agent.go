//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"gopkg.in/hraban/opus.v2"
)

const (
	opusRate  = 48000
	opusFrame = 960 // 20 ms at 48 kHz
)

// wsMsg is the /rtc envelope (internal/media/signaling/protocol.go), with only
// the fields the agent uses.
type wsMsg struct {
	Type      string          `json:"type"`
	Room      string          `json:"room,omitempty"`
	Name      string          `json:"name,omitempty"`
	SDP       string          `json:"sdp,omitempty"`
	Candidate json.RawMessage `json:"candidate,omitempty"`
	From      string          `json:"from,omitempty"`
	Auto      bool            `json:"auto,omitempty"`
	ToSip     string          `json:"to_sip,omitempty"`
	ToTeam    string          `json:"to_team,omitempty"`
	Reason    string          `json:"reason,omitempty"`
}

// sock is one /rtc WebSocket: serialized writes, and every message read
// pushed to msgs.
type sock struct {
	conn net.Conn
	wmu  sync.Mutex
	msgs chan wsMsg
	done chan struct{}
}

func dialSock(t *testing.T, gatewayURL, token string) *sock {
	t.Helper()
	u, err := url.Parse(gatewayURL)
	if err != nil {
		t.Fatalf("gateway url %q: %v", gatewayURL, err)
	}
	u.Scheme = "ws"
	u.Path = "/rtc"
	u.RawQuery = url.Values{"token": {token}}.Encode()
	conn, _, _, err := ws.Dial(context.Background(), u.String())
	if err != nil {
		t.Fatalf("ws dial %s: %v", u.Redacted(), err)
	}
	s := &sock{conn: conn, msgs: make(chan wsMsg, 64), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		for {
			b, err := wsutil.ReadServerText(conn)
			if err != nil {
				return
			}
			var m wsMsg
			if json.Unmarshal(b, &m) == nil {
				s.msgs <- m
			}
		}
	}()
	t.Cleanup(func() { _ = conn.Close() })
	return s
}

func (s *sock) send(m wsMsg) {
	b, _ := json.Marshal(m)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = wsutil.WriteClientMessage(s.conn, ws.OpText, b)
}

// keepalive sends m every 25 s until the socket closes.
func (s *sock) keepalive(m wsMsg) {
	go func() {
		tick := time.NewTicker(25 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-tick.C:
				s.send(m)
			}
		}
	}()
}

// Agent stands in for the SDK in an agent's browser: a presence socket that
// receives rings, and per call a call socket plus a pion PeerConnection that
// publishes the agent's tone and records what it hears.
type Agent struct {
	t       *testing.T
	api     *API
	id, sip string
	tone    float64
	pres    *sock
	call    *sock
	ctrl    chan wsMsg // call-socket messages other than offer/ice
	got     *Recorder

	mu       sync.Mutex
	silent   bool
	joinedAt time.Time
}

// NewAgent registers the agent's presence, as the SDK does on load, and waits
// until it is ringable.
func NewAgent(t *testing.T, api *API, agentID, sip string, tone float64) *Agent {
	t.Helper()
	token, gw := api.AgentToken(agentID)
	a := &Agent{t: t, api: api, id: agentID, sip: sip, tone: tone, got: &Recorder{}, ctrl: make(chan wsMsg, 64)}
	a.pres = dialSock(t, gw, token)
	a.pres.send(wsMsg{Type: "register"})
	a.pres.keepalive(wsMsg{Type: "register"})
	if m := a.next(a.pres, "ready", 10*time.Second); m.Type != "ready" {
		t.Fatalf("agent %s: no ready after register", sip)
	}
	api.WaitAgentOnline(agentID)
	return a
}

// next returns the next message of type typ on s, skipping others.
func (a *Agent) next(s *sock, typ string, timeout time.Duration) wsMsg {
	a.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case m := <-s.msgs:
			if m.Type == typ {
				return m
			}
		case <-deadline:
			a.t.Fatalf("agent %s: no %q within %v", a.sip, typ, timeout)
		}
	}
}

// WaitIncoming waits for a ring on the presence socket.
func (a *Agent) WaitIncoming(timeout time.Duration) (room, from string, auto bool) {
	a.t.Helper()
	m := a.next(a.pres, "incoming", timeout)
	return m.Room, m.From, m.Auto
}

// errNotLive: the PeerConnection never connected (in-process ICE stall); the
// scenario may retry it.
var errNotLive = errors.New("agent peer connection never connected")

// Join accepts a ring: mints a fresh token (the SDK does per join), joins the
// room on a new call socket, and answers the gateway's offers until the media
// connection is up.
func (a *Agent) Join(room string) error {
	a.t.Helper()
	token, gw := a.api.AgentToken(a.id)
	a.call = dialSock(a.t, gw, token)
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}
	a.t.Cleanup(func() { _ = pc.Close() })
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: opusRate, Channels: 2}, "audio", "agent-"+a.sip)
	if err != nil {
		return err
	}
	if _, err := pc.AddTrack(track); err != nil {
		return err
	}
	connected := make(chan struct{})
	var once sync.Once
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateConnected {
			once.Do(func() {
				a.mu.Lock()
				a.joinedAt = time.Now()
				a.mu.Unlock()
				close(connected)
			})
		}
	})
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		b, _ := json.Marshal(c.ToJSON())
		a.call.send(wsMsg{Type: "ice", Candidate: b})
	})
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { a.record(tr) })
	go a.signal(pc, a.call)
	go a.publish(track, a.call)

	a.call.send(wsMsg{Type: "join", Room: room, Name: a.sip})
	a.call.keepalive(wsMsg{Type: "ping"})
	select {
	case <-connected:
		return nil
	case <-time.After(10 * time.Second):
		return errNotLive
	}
}

// signal answers the gateway's offers and applies its ICE candidates; every
// other call-socket message is kept for WaitMsg.
func (a *Agent) signal(pc *webrtc.PeerConnection, call *sock) {
	var pending []webrtc.ICECandidateInit
	haveRemote := false
	for {
		var m wsMsg
		select {
		case m = <-call.msgs:
		case <-call.done:
			return
		}
		switch m.Type {
		case "offer":
			if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: m.SDP}); err != nil {
				a.t.Logf("agent %s: set offer: %v", a.sip, err)
				continue
			}
			haveRemote = true
			for _, c := range pending {
				_ = pc.AddICECandidate(c)
			}
			pending = nil
			ans, err := pc.CreateAnswer(nil)
			if err == nil {
				err = pc.SetLocalDescription(ans)
			}
			if err != nil {
				a.t.Logf("agent %s: answer: %v", a.sip, err)
				continue
			}
			call.send(wsMsg{Type: "answer", SDP: ans.SDP})
		case "ice":
			var c webrtc.ICECandidateInit
			if json.Unmarshal(m.Candidate, &c) != nil {
				continue
			}
			if haveRemote {
				_ = pc.AddICECandidate(c)
			} else {
				pending = append(pending, c)
			}
		default:
			a.ctrl <- m
		}
	}
}

// publish sends the agent's tone (or silence while held, as a muted Chrome
// does) as 20 ms Opus frames.
func (a *Agent) publish(track *webrtc.TrackLocalStaticSample, call *sock) {
	enc, err := opus.NewEncoder(opusRate, 1, opus.AppVoIP)
	if err != nil {
		a.t.Errorf("opus encoder: %v", err)
		return
	}
	buf := make([]byte, 1500)
	var phase float64
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-call.done:
			return
		case <-tick.C:
		}
		pcm := Tone(a.tone, opusRate, opusFrame, &phase)
		a.mu.Lock()
		if a.silent {
			pcm = make([]int16, opusFrame)
		}
		a.mu.Unlock()
		n, err := enc.Encode(pcm, buf)
		if err != nil {
			continue
		}
		_ = track.WriteSample(media.Sample{Data: append([]byte(nil), buf[:n]...), Duration: 20 * time.Millisecond})
	}
}

// record decodes every received Opus packet into the agent's recorder.
func (a *Agent) record(tr *webrtc.TrackRemote) {
	dec, err := opus.NewDecoder(opusRate, 1)
	if err != nil {
		a.t.Errorf("opus decoder: %v", err)
		return
	}
	pcm := make([]int16, opusFrame*6)
	for {
		pkt, _, err := tr.ReadRTP()
		if err != nil {
			return
		}
		n, err := dec.Decode(pkt.Payload, pcm)
		if err != nil {
			n = 0
		}
		a.got.Add(Arrival{At: time.Now(), Src: tr.StreamID(), Seq: pkt.SequenceNumber, TS: pkt.Timestamp, Marker: pkt.Marker, PCM: append([]int16(nil), pcm[:n]...)})
	}
}

// Got is what the agent has heard. JoinedAt is when its media came up.
func (a *Agent) Got() *Recorder { return a.got }

func (a *Agent) JoinedAt() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.joinedAt
}

// Hold puts the customer on hold the way the widget does, and starts sending
// silence as a muted mic would; Resume undoes both.
func (a *Agent) Hold() {
	a.setSilent(true)
	a.call.send(wsMsg{Type: "hold"})
}

func (a *Agent) Resume() {
	a.call.send(wsMsg{Type: "resume"})
	a.setSilent(false)
}

func (a *Agent) setSilent(s bool) {
	a.mu.Lock()
	a.silent = s
	a.mu.Unlock()
}

func (a *Agent) TransferToAgent(sip string) { a.call.send(wsMsg{Type: "transfer", ToSip: sip}) }
func (a *Agent) TransferToTeam(team string) { a.call.send(wsMsg{Type: "transfer", ToTeam: team}) }

// WaitMsg waits for a call-socket message of type typ (answered, transfer-ok,
// ended, …).
func (a *Agent) WaitMsg(typ string, timeout time.Duration) wsMsg {
	a.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case m := <-a.ctrl:
			if m.Type == typ {
				return m
			}
		case <-deadline:
			a.t.Fatalf("agent %s: no %q on the call socket within %v", a.sip, typ, timeout)
		}
	}
}

// Hangup ends the agent's call leg.
func (a *Agent) Hangup() {
	if a.call != nil {
		a.call.send(wsMsg{Type: "bye"})
	}
}
