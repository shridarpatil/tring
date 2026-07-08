package fsplug

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"
	"github.com/shridarpatil/tring/internal/media/room"
)

// ringTimeout bounds how long onInvite blocks waiting for Answer/Reject.
// sipgo's Server.handleRequest calls tx.TerminateGracefully() the moment
// the OnInvite handler returns (see server.go), which force-terminates a
// non-finalized INVITE transaction. Since our OnInbound consumer answers
// asynchronously (it resolves the DID/routes the call before deciding),
// onInvite must block until that decision lands rather than returning
// immediately — otherwise the transaction is gone before Answer/Reject
// ever gets to respond. Each INVITE is handled in its own goroutine
// (sip/transaction_layer.go: handleRequestBackground), so this only
// delays the one call, not the rest of the server.
const ringTimeout = 30 * time.Second

// Config is the plug's wiring, built from koanf in cmd/gateway.
type Config struct {
	SIPListen    string // UAS bind, e.g. ":5080"
	SIPAdvertise string // IP put in SDP/Contact, e.g. "127.0.0.1"
	RTPMin       int
	RTPMax       int
	OpusPT       uint8  // e.g. 111
	DTMFPT       uint8  // e.g. 101
	FSSIP        string // FreeSWITCH SIP addr for outbound INVITEs, e.g. "127.0.0.1:5060"
}

// call is one in-flight inbound dialog + its media.
type call struct {
	sess    *sipgo.DialogServerSession
	tx      sip.ServerTransaction
	media   *mediaSession
	offerIP string
	offerPt int

	// responded is closed once Answer or Reject has sent the final
	// response, unblocking onInvite's wait (see ringTimeout above).
	responded   chan struct{}
	respondOnce sync.Once
}

func (c *call) markResponded() {
	c.respondOnce.Do(func() { close(c.responded) })
}

// Plug implements room.Plug over FreeSWITCH via SIP + plain RTP.
type Plug struct {
	cfg    Config
	log    *slog.Logger
	ua     *sipgo.UserAgent
	srv    *sipgo.Server
	client *sipgo.Client
	ds     *sipgo.DialogServerCache
	dc     *sipgo.DialogClientCache

	// contact is stamped onto our 2xx response when we bypass
	// DialogServerSession.RespondSDP (see Answer's doc comment).
	contact sip.ContactHeader

	onInbound func(room.InboundCall)

	mu       sync.Mutex
	calls    map[room.CallID]*call
	outbound map[room.ParticipantID]*outboundCall
}

// outboundCall pairs a room-initiated outbound dialog with its media
// session so HangupOutbound can BYE the carrier and free the RTP socket
// together, keyed by the leg's ParticipantID (what the room/signaling
// layer knows the leg by — leg.Source.ID()).
type outboundCall struct {
	dlg *sipgo.DialogClientSession
	m   *mediaSession
}

func New(cfg Config, log *slog.Logger) (*Plug, error) {
	ua, err := sipgo.NewUA()
	if err != nil {
		return nil, err
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		return nil, err
	}
	client, err := sipgo.NewClient(ua)
	if err != nil {
		return nil, err
	}
	// Contact must carry our SIP port, not just the advertised IP — the
	// client uses it verbatim as the Request-URI for in-dialog requests
	// (BYE), so a portless Contact sends those to host:0.
	_, portStr, err := net.SplitHostPort(cfg.SIPListen)
	if err != nil {
		return nil, fmt.Errorf("fsplug: invalid SIPListen %q: %w", cfg.SIPListen, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("fsplug: invalid SIPListen port %q: %w", portStr, err)
	}
	contact := sip.ContactHeader{Address: sip.Uri{User: "tring", Host: cfg.SIPAdvertise, Port: port}}
	p := &Plug{
		cfg: cfg, log: log, ua: ua, srv: srv, client: client,
		ds:      sipgo.NewDialogServerCache(client, contact),
		dc:      sipgo.NewDialogClientCache(client, contact),
		contact:  contact,
		calls:    map[room.CallID]*call{},
		outbound: map[room.ParticipantID]*outboundCall{},
	}
	srv.OnInvite(p.onInvite)
	srv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) { _ = p.ds.ReadAck(req, tx) })
	// A BYE can belong to either cache: an inbound (FS-originated) dialog is
	// ours as a server, an outbound (Tring-originated) dialog is ours as a
	// client. Try the server cache first; if it doesn't recognize the dialog,
	// fall through to the client cache. Without this, an outbound call's
	// carrier-hangup BYE is silently dropped and Leg.Done never fires.
	srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		if err := p.ds.ReadBye(req, tx); err != nil {
			_ = p.dc.ReadBye(req, tx)
		}
	})
	return p, nil
}

// Run serves SIP until ctx is cancelled.
func (p *Plug) Run(ctx context.Context) error {
	laddr, err := net.ResolveUDPAddr("udp", p.cfg.SIPListen)
	if err != nil {
		return fmt.Errorf("fsplug: resolve sip listen %q: %w", p.cfg.SIPListen, err)
	}
	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return fmt.Errorf("fsplug: listen udp %q: %w", p.cfg.SIPListen, err)
	}
	// We own the listener lifecycle: closing conn on ctx cancel unblocks
	// ServeUDP. We deliberately do NOT use srv.ListenAndServe — its ctx-cancel
	// path reads an unsynchronized connCloser var (sipgo v1.4.0 data race under
	// -race), and srv.Close() is a no-op. conn is captured after assignment
	// (happens-before via goroutine start); *net.UDPConn is safe for concurrent
	// Close/Read, so this is race-free.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	err = p.srv.ServeUDP(conn)
	if ctx.Err() != nil {
		return nil // graceful shutdown (conn closed by our goroutine)
	}
	return err
}

func (p *Plug) OnInbound(fn func(room.InboundCall)) {
	p.mu.Lock()
	p.onInbound = fn
	p.mu.Unlock()
}

// onInvite maps an incoming INVITE to an InboundCall notification. It does NOT
// answer — the signaling server decides (Answer/Reject) after resolving the DID.
func (p *Plug) onInvite(req *sip.Request, tx sip.ServerTransaction) {
	sess, err := p.ds.ReadInvite(req, tx)
	if err != nil {
		p.log.Error("fsplug: ReadInvite failed", "err", err)
		return
	}
	ip, port, err := parseOffer(req.Body())
	if err != nil {
		p.log.Warn("fsplug: bad offer, rejecting", "err", err)
		_ = sess.Respond(sip.StatusBadRequest, "Bad SDP", nil)
		return
	}
	id := room.CallID(req.CallID().Value())
	c := &call{sess: sess, tx: tx, offerIP: ip, offerPt: port, responded: make(chan struct{})}
	p.mu.Lock()
	p.calls[id] = c
	cb := p.onInbound
	p.mu.Unlock()

	ic := room.InboundCall{CallID: id, From: req.From().Address.User, To: req.To().Address.User}
	if cb != nil {
		cb(ic)
	}

	// Block until Answer/Reject responds, or time the call out — see
	// ringTimeout's doc comment for why this wait is required at all.
	select {
	case <-c.responded:
	case <-time.After(ringTimeout):
		_ = sess.Respond(sip.StatusRequestTimeout, "Ring Timeout", nil)
		p.forget(id)
	}
}

func (p *Plug) get(id room.CallID) *call {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[id]
}

// Answer accepts the call: allocates media, sends a 200 with an Opus-only SDP,
// and returns a room.Leg bridging the RTP socket to the room.
func (p *Plug) Answer(id room.CallID) (*room.Leg, error) {
	c := p.get(id)
	if c == nil {
		return nil, fmt.Errorf("fsplug: unknown call %s", id)
	}
	m, err := newMediaSession(room.ParticipantID(id), p.cfg.SIPAdvertise, p.cfg.RTPMin, p.cfg.RTPMax, p.cfg.OpusPT, p.cfg.DTMFPT)
	if err != nil {
		return nil, err
	}
	m.setRemote(c.offerIP, c.offerPt)
	p.mu.Lock()
	c.media = m
	p.mu.Unlock()

	answer := buildAnswer(p.cfg.SIPAdvertise, m.localPort(), p.cfg.OpusPT, p.cfg.DTMFPT)

	// Deliberately NOT c.sess.RespondSDP(answer): DialogServerSession.WriteResponse
	// (which RespondSDP wraps) sends the 200 and then BLOCKS the caller for up to
	// 64*T1 (~32s) per RFC 3261 §13.3.1.4, retransmitting until the far end's ACK
	// arrives. That ACK is only sent once the far end's application explicitly
	// answers + acks (unlike error responses, which sipgo's client auto-ACKs) — so
	// a caller who Answers and then separately waits/ACKs on its own goroutine
	// would deadlock against itself here. We build the same response WriteResponse
	// would and send it on the raw sip.ServerTransaction, which just transmits and
	// returns; the ACK is still tracked (asynchronously) by our own OnAck handler.
	res := sip.NewSDPResponseFromRequest(c.sess.InviteRequest, answer)
	if res.Contact() == nil {
		res.AppendHeader(&p.contact)
	}
	c.sess.InviteResponse = res

	// Register the dialog state channel BEFORE transmitting the 200, mirroring
	// sipgo's own DialogServerSession.WriteResponse (dialog_server.go): if a BYE
	// races the response and lands before we subscribe, StateRead() would only
	// ever see future transitions and watchDialog would range forever, leaking
	// the goroutine + mediaSession.
	stateCh := c.sess.StateRead()
	err = c.tx.Respond(res)
	c.markResponded() // unblock onInvite's wait regardless of outcome
	if err != nil {
		m.Close()
		return nil, fmt.Errorf("fsplug: respond 200 failed: %w", err)
	}

	// Watch the SIP dialog for a remote BYE and fire the leg's Done.
	go p.watchDialog(id, c, m, stateCh)

	return &room.Leg{
		Source: m,
		Write:  func(pkt *rtp.Packet) error { return m.write(pkt) },
		DTMF:   m.dtmf(),
		Done:   m.done(),
	}, nil
}

// watchDialog closes the media session (firing Leg.Done) when the SIP dialog
// ends (remote BYE). stateCh must be obtained via c.sess.StateRead() BEFORE
// the 200 was sent (see Answer) so an immediate BYE can't be missed.
func (p *Plug) watchDialog(id room.CallID, c *call, m *mediaSession, stateCh <-chan sip.DialogState) {
	for state := range stateCh {
		if state == sip.DialogStateEnded {
			break
		}
	}
	p.forget(id)
	m.Close()
}

func (p *Plug) Reject(id room.CallID) error {
	c := p.get(id)
	if c == nil {
		return fmt.Errorf("fsplug: unknown call %s", id)
	}
	err := c.sess.Respond(sip.StatusTemporarilyUnavailable, "No Flow", nil)
	c.markResponded()
	p.forget(id)
	return err
}

func (p *Plug) Hangup(id room.CallID) error {
	p.mu.Lock()
	c, ok := p.calls[id]
	var media *mediaSession
	if ok {
		media = c.media
	}
	p.mu.Unlock()
	if !ok {
		return nil // already gone
	}

	// Never answered: c.sess.InviteResponse is nil (only set in Answer), and
	// sipgo's WriteBye unconditionally calls res.IsSuccess() on it, which
	// nil-pointer-panics. media is set only in Answer, so its absence is our
	// reliable "was this answered?" signal — skip Bye entirely in that case.
	if media == nil {
		p.forget(id)
		return nil
	}

	err := c.sess.Bye(context.Background())
	media.Close()
	p.forget(id)
	return err
}

func (p *Plug) forget(id room.CallID) {
	p.mu.Lock()
	delete(p.calls, id)
	p.mu.Unlock()
}

// HangupOutbound BYEs the carrier for a room-initiated outbound call,
// identified by its leg's ParticipantID (leg.Source.ID(), which is what
// Originate registered it under). This is the room-hangs-up-first
// direction: Originate's own teardown goroutine already handles the
// carrier-hangs-up-first direction by firing Leg.Done, and that goroutine
// (or its residual-guard sibling) is exactly what unregisters this map
// entry on that path — so a pid that's already gone here means the
// carrier beat us to it, not an error. Idempotent by design.
func (p *Plug) HangupOutbound(pid room.ParticipantID) error {
	p.mu.Lock()
	oc, ok := p.outbound[pid]
	delete(p.outbound, pid)
	p.mu.Unlock()
	if !ok {
		return nil // already torn down (carrier hung up first, or double-call)
	}

	// Bye and Close happen outside the lock: both can block/take time
	// (network round-trip, socket teardown) and neither touches p's state.
	err := oc.dlg.Bye(context.Background())
	oc.m.Close()
	return err
}

func (p *Plug) forgetOutbound(pid room.ParticipantID) {
	p.mu.Lock()
	delete(p.outbound, pid)
	p.mu.Unlock()
}
