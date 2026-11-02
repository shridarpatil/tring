//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
)

const (
	carrierSIPPort = 5999
	// freqRingback is the carrier's early-media tone: deliberately not in
	// Freqs, so leaked ringback reads as "no clear tone", never as a party.
	freqRingback = 425.0
	pcmuFrame    = 160 // 20 ms at 8 kHz
)

// Carrier is a fake SIP carrier on FreeSWITCH's trunk side: a UAS that
// answers Tring's outbound legs (with early-media ringback first) and a UAC
// that places inbound calls. Every leg plays its own tone as PCMU and records
// what it receives.
type Carrier struct {
	t       *testing.T
	ip      string
	client  *sipgo.Client
	ds      *sipgo.DialogServerCache
	dc      *sipgo.DialogClientCache
	RingFor time.Duration

	mu      sync.Mutex
	numbers map[string]*Leg // RURI user → the leg to answer it with
	byCall  map[string]*Leg // SIP Call-ID → leg
}

// carrierIP is FreeSWITCH's trunk-facing address, which up.sh exports.
func carrierIP(t *testing.T) string {
	ip := env("TRING_E2E_TRUNK_IP", "")
	if ip == "" {
		t.Fatal("TRING_E2E_TRUNK_IP unset (source .e2e.env from e2e/up.sh)")
	}
	return ip
}

// NewCarrier starts the carrier's SIP stack on ip:5999 until the test ends.
func NewCarrier(t *testing.T, ip string) *Carrier {
	t.Helper()
	ua, err := sipgo.NewUA(sipgo.WithUserAgentHostname(ip))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatal(err)
	}
	client, err := sipgo.NewClient(ua, sipgo.WithClientHostname(ip))
	if err != nil {
		t.Fatal(err)
	}
	contact := sip.ContactHeader{Address: sip.Uri{User: "carrier", Host: ip, Port: carrierSIPPort}}
	c := &Carrier{
		t: t, ip: ip, client: client, RingFor: 3 * time.Second,
		ds:      sipgo.NewDialogServerCache(client, contact),
		dc:      sipgo.NewDialogClientCache(client, contact),
		numbers: map[string]*Leg{},
		byCall:  map[string]*Leg{},
	}
	srv.OnOptions(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
	})
	srv.OnInvite(c.onInvite)
	srv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) { _ = c.ds.ReadAck(req, tx) })
	srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		if err := c.ds.ReadBye(req, tx); err != nil {
			_ = c.dc.ReadBye(req, tx)
		}
		if l := c.leg(req.CallID().Value()); l != nil {
			l.end()
		}
	})
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(ip), Port: carrierSIPPort})
	if err != nil {
		t.Fatalf("carrier: listen %s:%d: %v", ip, carrierSIPPort, err)
	}
	go func() { _ = srv.ServeUDP(conn) }()
	t.Cleanup(func() {
		c.mu.Lock()
		legs := make([]*Leg, 0, len(c.byCall))
		for _, l := range c.byCall {
			legs = append(legs, l)
		}
		c.mu.Unlock()
		for _, l := range legs {
			l.Hangup()
		}
		_ = conn.Close()
	})
	return c
}

// Addr is the carrier's SIP address, the trunk's proxy.
func (c *Carrier) Addr() string { return c.ip + ":" + strconv.Itoa(carrierSIPPort) }

func (c *Carrier) leg(callID string) *Leg {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byCall[callID]
}

// OnNumber registers the tone the carrier plays when Tring dials ruriUser
// (e.g. "+919800000001") and returns that leg, live once the INVITE arrives.
func (c *Carrier) OnNumber(ruriUser string, tone float64) *Leg {
	l := newLeg(c.t, c.ip, tone)
	c.mu.Lock()
	c.numbers[ruriUser] = l
	c.mu.Unlock()
	return l
}

func (c *Carrier) onInvite(req *sip.Request, tx sip.ServerTransaction) {
	user := req.Recipient.User
	c.mu.Lock()
	l := c.numbers[user]
	delete(c.numbers, user)
	if l != nil {
		c.byCall[req.CallID().Value()] = l
	}
	c.mu.Unlock()
	if l == nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusNotFound, "No such test number", nil))
		return
	}
	dlg, err := c.ds.ReadInvite(req, tx)
	if err != nil {
		c.t.Errorf("carrier: read invite for %s: %v", user, err)
		return
	}
	ip, port, err := parseSDP(req.Body())
	if err != nil {
		c.t.Errorf("carrier: invite for %s: %v", user, err)
		return
	}
	l.setRemote(ip, port)
	l.server = dlg
	answer := l.sdp()
	_ = dlg.Respond(sip.StatusTrying, "Trying", nil)
	_ = dlg.Respond(183, "Session Progress", answer, sip.NewHeader("Content-Type", "application/sdp"))
	l.play(freqRingback)
	time.Sleep(c.RingFor) // the customer's phone ringing (early media flowing)
	if err := dlg.RespondSDP(answer); err != nil {
		c.t.Errorf("carrier: answer %s: %v", user, err)
		return
	}
	l.answer()
}

// Dial places an inbound call: INVITE did@fsExternal, offering PCMU.
func (c *Carrier) Dial(fsExternal, did string, tone float64) *Leg {
	c.t.Helper()
	l := newLeg(c.t, c.ip, tone)
	host, port := splitHostPort(fsExternal)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dlg, err := c.dc.Invite(ctx, sip.Uri{User: did, Host: host, Port: port}, l.sdp(), sip.NewHeader("Content-Type", "application/sdp"))
	if err != nil {
		c.t.Fatalf("carrier: invite %s: %v", did, err)
	}
	c.mu.Lock()
	c.byCall[dlg.InviteRequest.CallID().Value()] = l
	c.mu.Unlock()
	if err := dlg.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
		c.t.Fatalf("carrier: %s not answered: %v", did, err)
	}
	ip, rport, err := parseSDP(dlg.InviteResponse.Body())
	if err != nil {
		c.t.Fatalf("carrier: answer for %s: %v", did, err)
	}
	l.setRemote(ip, rport)
	if err := dlg.Ack(ctx); err != nil {
		c.t.Fatalf("carrier: ack %s: %v", did, err)
	}
	l.client = dlg
	l.answer()
	return l
}

// Leg is one carrier call leg: its RTP socket, what it plays, what it got.
type Leg struct {
	t        *testing.T
	conn     *net.UDPConn
	tone     float64
	Answered chan struct{}
	Done     chan struct{}
	Got      *Recorder

	mu         sync.Mutex
	remote     *net.UDPAddr
	playing    float64 // tone being sent; 0 = nothing yet
	answeredAt time.Time
	server     *sipgo.DialogServerSession
	client     *sipgo.DialogClientSession
	once       sync.Once
}

func newLeg(t *testing.T, ip string, tone float64) *Leg {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(ip)})
	if err != nil {
		t.Fatalf("carrier: rtp socket: %v", err)
	}
	l := &Leg{t: t, conn: conn, tone: tone, Answered: make(chan struct{}), Done: make(chan struct{}), Got: &Recorder{}}
	go l.recv()
	go l.send()
	return l
}

func (l *Leg) sdp() []byte {
	ip := l.conn.LocalAddr().(*net.UDPAddr)
	return []byte(fmt.Sprintf("v=0\r\no=carrier 1 1 IN IP4 %s\r\ns=-\r\nc=IN IP4 %s\r\nt=0 0\r\n"+
		"m=audio %d RTP/AVP 0 101\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-16\r\na=ptime:20\r\n",
		ip.IP, ip.IP, ip.Port))
}

func (l *Leg) setRemote(ip string, port int) {
	l.mu.Lock()
	l.remote = &net.UDPAddr{IP: net.ParseIP(ip), Port: port}
	l.mu.Unlock()
}

func (l *Leg) play(freq float64) {
	l.mu.Lock()
	l.playing = freq
	l.mu.Unlock()
}

func (l *Leg) answer() {
	l.mu.Lock()
	l.answeredAt = time.Now()
	l.playing = l.tone
	l.mu.Unlock()
	close(l.Answered)
}

// AnsweredAt is when this leg answered (zero before).
func (l *Leg) AnsweredAt() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.answeredAt
}

// send paces PCMU frames of the current tone every 20 ms once a remote is set.
func (l *Leg) send() {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var phase float64
	var seq uint16
	var ts uint32
	for {
		select {
		case <-l.Done:
			return
		case <-tick.C:
		}
		l.mu.Lock()
		remote, freq := l.remote, l.playing
		l.mu.Unlock()
		if remote == nil || freq == 0 {
			continue
		}
		seq++
		ts += pcmuFrame
		pkt := &rtp.Packet{
			Header:  rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: seq, Timestamp: ts, SSRC: 0x63617272},
			Payload: MuLawEncode(Tone(freq, 8000, pcmuFrame, &phase)),
		}
		if raw, err := pkt.Marshal(); err == nil {
			_, _ = l.conn.WriteToUDP(raw, remote)
		}
	}
}

// recv records every PCMU packet that arrives, decoded.
func (l *Leg) recv() {
	buf := make([]byte, 1500)
	for {
		n, err := l.conn.Read(buf)
		if err != nil {
			return
		}
		var pkt rtp.Packet
		if pkt.Unmarshal(buf[:n]) != nil || pkt.PayloadType != 0 {
			continue
		}
		l.Got.Add(Arrival{At: time.Now(), Seq: pkt.SequenceNumber, TS: pkt.Timestamp, Marker: pkt.Marker, PCM: MuLawDecode(pkt.Payload)})
	}
}

func (l *Leg) end() {
	l.once.Do(func() {
		close(l.Done)
		_ = l.conn.Close()
	})
}

// Hangup sends BYE (if the call is up) and ends the leg.
func (l *Leg) Hangup() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	l.mu.Lock()
	s, c := l.server, l.client
	l.mu.Unlock()
	select {
	case <-l.Done:
	default:
		if s != nil {
			_ = s.Bye(ctx)
		}
		if c != nil {
			_ = c.Bye(ctx)
		}
	}
	l.end()
}

// parseSDP extracts the audio media address (copied from internal/fsplug's
// unexported parseOffer).
func parseSDP(body []byte) (string, int, error) {
	var desc sdp.SessionDescription
	if err := desc.Unmarshal(body); err != nil {
		return "", 0, fmt.Errorf("bad SDP: %w", err)
	}
	ip := ""
	if desc.ConnectionInformation != nil && desc.ConnectionInformation.Address != nil {
		ip = desc.ConnectionInformation.Address.Address
	}
	for _, md := range desc.MediaDescriptions {
		if md.MediaName.Media != "audio" {
			continue
		}
		if md.ConnectionInformation != nil && md.ConnectionInformation.Address != nil {
			ip = md.ConnectionInformation.Address.Address
		}
		if ip == "" {
			return "", 0, fmt.Errorf("no connection address for audio")
		}
		return ip, md.MediaName.Port.Value, nil
	}
	return "", 0, fmt.Errorf("no audio media")
}

// splitHostPort splits "ip:port", defaulting to 5060 (copied from
// internal/fsplug).
func splitHostPort(addr string) (string, int) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, 5060
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return host, 5060
	}
	return host, port
}
