package gateway

import (
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// FSConfig points the gateway at a (private) FreeSWITCH it bridges calls into.
// The gateway is a SIP B2BUA toward FS: it digest-authenticates as a directory
// user and INVITEs a dialplan extension; media is plain RTP on the private
// network (no DTLS on the FS leg).
type FSConfig struct {
	SIPAddr  string // FS internal profile host:port, e.g. "127.0.0.1:5060"
	AuthUser string // directory user to auth as, e.g. "1000"
	AuthPass string // its SIP password
	Target   string // dialplan extension to call when no flow is pinned (echo/DID)
	// FlowID pins the call to an IVR flow directly (no DID/numbers lookup) —
	// the web-call path. When set, the gateway routes to the flow-pinned
	// dialplan extension and passes the id as a SIP header the dialplan maps
	// to tring_flow_id (mirrors the guest web-calling path). Empty → Target.
	FlowID   string
	AdvertIP string // IP advertised to FS for SIP+RTP (FS-reachable), e.g. "127.0.0.1"
	SIPPort  int    // gateway SIP listen port, e.g. 5090
}

// gwFlowExtension is the dialplan destination that pins an IVR flow from the
// X-Tring-Flow-Id SIP header (see freeswitch/conf/dialplan/default.xml).
const gwFlowExtension = "gwflow"

// fsClient owns the shared sipgo stack (one UA + client + server listener),
// reused for every call leg. It is both a UAC toward FS (visitor path: the
// gateway dials FS) and a UAS (agent path: FS dials the gateway). Built once in
// gateway.New when FS is configured.
type fsClient struct {
	cfg    FSConfig
	srv    *sipgo.Server
	client *sipgo.Client            // raw UAC client — used to WriteRequest orphan ACKs
	dc     *sipgo.DialogClientCache // UAC dialogs (gateway → FS)
	ds     *sipgo.DialogServerCache // UAS dialogs (FS → gateway, agent legs)
	bind   string
	log    *log.Logger
}

func newFSClient(cfg FSConfig, logger *log.Logger) (*fsClient, error) {
	f := &fsClient{cfg: cfg, log: logger, bind: fmt.Sprintf(":%d", cfg.SIPPort)}
	// The UA hostname drives the From header domain (defaults to "localhost").
	// FreeSWITCH's auto_from challenge realm derives from it, so it MUST be the
	// directory domain where the auth user lives, or FS looks up the user under
	// the wrong domain and 403s ("Can't find user [1000@localhost]").
	//
	// The unhandled-response handler ACKs stray 2xx INVITE responses — FS
	// retransmitting a 200 OK after our client transaction is already gone (a
	// teardown race). Without an ACK, FS keeps retransmitting on the INVITE
	// timer for ~32s and holds the channel, which stretches out the disconnect.
	// See ackOrphan2xx.
	ua, err := sipgo.NewUA(
		sipgo.WithUserAgentHostname(cfg.AdvertIP),
		sipgo.WithUserAgentTransactionLayerOptions(
			sip.WithTransactionLayerUnhandledResponseHandler(f.ackOrphan2xx),
		),
	)
	if err != nil {
		return nil, err
	}
	// Listen on ALL interfaces, not just AdvertIP. FreeSWITCH originates agent
	// legs through the `tring-gw` gateway whose proxy is tring_gateway_sip
	// (default 127.0.0.1:5090); binding only AdvertIP would miss INVITEs sent
	// to 127.0.0.1. Via/Contact host stays AdvertIP (below) for the visitor
	// leg's digest realm, so both directions work without a per-host vars.xml edit.
	// (f.bind = ":SIPPort" was set above.)
	//
	// Use ONLY WithClientHostname (sets Via/Contact host = AdvertIP for FS's
	// digest realm) and let the UAC REUSE the UAS listener socket: sipgo's
	// transport defaults to ConnectionReuse=true, and the client takes its Via
	// port from the listener (GetListenPort = SIPPort). So the INVITE goes out
	// from the same :SIPPort the server listens on, and FS's 407/responses
	// return to that socket and match the client transaction.
	//   - WithClientAddr/WithClientPort would force an ephemeral source port
	//     with ConnectionReuse=false → responses hit the listener, never the
	//     client txn → Timer_B ("transaction terminated").
	//   - WithClientConnectionAddr would bind a SECOND socket on :SIPPort →
	//     "address already in use" against the server listener.
	client, err := sipgo.NewClient(ua, sipgo.WithClientHostname(cfg.AdvertIP))
	if err != nil {
		return nil, err
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		return nil, err
	}
	// From/Contact user must be the directory user we digest-auth as, or FS
	// rejects the INVITE (403) — it authenticates against the From user.
	contact := sip.ContactHeader{Address: sip.Uri{User: cfg.AuthUser, Host: cfg.AdvertIP, Port: cfg.SIPPort}}
	f.srv = srv
	f.client = client
	f.dc = sipgo.NewDialogClientCache(client, contact)
	f.ds = sipgo.NewDialogServerCache(client, contact)
	return f, nil
}

// ackOrphan2xx ACKs a 2xx INVITE response that arrived with no matching client
// transaction — typically FreeSWITCH retransmitting a 200 OK after our
// transaction has already terminated (a teardown race). sipgo would otherwise
// just log it as "Unhandled sip response", and FS keeps retransmitting on the
// INVITE 200-OK timer (~every 4s up to ~32s), holding the channel open. RFC 3261
// §13.2.2.4 requires a UAC core to ACK a 2xx even absent a transaction; doing so
// makes FS release the leg immediately. Only INVITE 2xx are retransmitted, so we
// ignore everything else (matching the default no-op).
func (f *fsClient) ackOrphan2xx(resp *sip.Response) {
	cseq := resp.CSeq()
	if cseq == nil || cseq.MethodName != sip.INVITE {
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return
	}
	from, to, callID := resp.From(), resp.To(), resp.CallID()
	if from == nil || to == nil || callID == nil {
		return
	}
	// Request-URI = the remote target (Contact in the 2xx), else the To address.
	recipient := to.Address
	if c := resp.Contact(); c != nil {
		recipient = c.Address
	}
	ack := sip.NewRequest(sip.ACK, *recipient.Clone())
	ack.AppendHeader(sip.HeaderClone(from))
	ack.AppendHeader(sip.HeaderClone(to)) // carries FS's To-tag (dialog id)
	ack.AppendHeader(sip.HeaderClone(callID))
	ackCSeq := sip.HeaderClone(cseq).(*sip.CSeqHeader)
	ackCSeq.MethodName = sip.ACK
	ack.AppendHeader(ackCSeq)
	mf := sip.MaxForwardsHeader(70)
	ack.AppendHeader(&mf)
	// Send back to where the response came from (FS), over the same transport.
	// WriteRequest (no opts) fills in the mandatory Via/Contact for us.
	ack.SetTransport(resp.Transport())
	ack.SetSource(resp.Destination())
	ack.SetDestination(resp.Source())
	if err := f.client.WriteRequest(ack); err != nil {
		f.log.Printf("gateway: orphan 2xx ACK failed: %v", err)
		return
	}
	f.log.Printf("gateway: ACKed orphan 2xx (call-id %s) — stopping FS retransmits", callID.Value())
}

// serve starts the SIP listener. Call AFTER any UAS handlers are registered on
// f.srv so inbound INVITEs (agent legs) are dispatched. The listener also
// receives UAC in-dialog requests (BYE from FS).
func (f *fsClient) serve(ctx context.Context) {
	go func() { _ = f.srv.ListenAndServe(ctx, "udp", f.bind) }()
}

// fsLeg is one established call into FreeSWITCH: a UDP RTP socket bound on the
// gateway, the negotiated FS RTP endpoint, and the SIP dialog.
type fsLeg struct {
	RTP       *net.UDPConn
	FSRTPAddr *net.UDPAddr
	OpusPT    uint8 // payload type FS negotiated for Opus
	DTMFPT    uint8 // payload type FS negotiated for telephone-event (DTMF)
	session   *sipgo.DialogClientSession
	log       *log.Logger
	closeOnce sync.Once
}

// dial INVITEs the target, authenticates, and returns the live leg with a bound
// RTP socket and FS's RTP address. flowID, when non-empty, overrides the
// configured default flow (the SDK supplies it per call) so the call pins to
// that IVR flow. Caller must Close the returned leg.
func (f *fsClient) dial(ctx context.Context, flowID, clientRef string) (*fsLeg, error) {
	if flowID == "" {
		flowID = f.cfg.FlowID
	}
	// Bind the gateway RTP socket first so we can advertise its port in the SDP.
	rtp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(f.cfg.AdvertIP), Port: 0})
	if err != nil {
		return nil, fmt.Errorf("rtp socket: %w", err)
	}
	localPort := rtp.LocalAddr().(*net.UDPAddr).Port
	f.log.Printf("gateway: our RTP socket on %s:%d (advertised to FS)", f.cfg.AdvertIP, localPort)

	offer := buildSDP(f.cfg.AdvertIP, localPort, 111, 101)
	host, portStr, _ := net.SplitHostPort(f.cfg.SIPAddr)
	port, _ := strconv.Atoi(portStr)

	// Flow-pinned (web call): route to the gwflow extension and carry the flow
	// id in a SIP header — no DID. Otherwise dial the configured Target
	// (echo/DID). FreeSWITCH exposes X-… headers as ${sip_h_X-…}.
	dest := f.cfg.Target
	headers := []sip.Header{sip.NewHeader("Content-Type", "application/sdp")}
	if flowID != "" {
		dest = gwFlowExtension
		headers = append(headers, sip.NewHeader("X-Tring-Flow-Id", flowID))
		// Trusted caller identity (from the gateway call token) — the dialplan
		// maps it to tring_client_ref so the IVR executor records it on the CDR.
		if clientRef != "" {
			headers = append(headers, sip.NewHeader("X-Tring-Client-Ref", clientRef))
		}
	}
	recipient := sip.Uri{User: dest, Host: host, Port: port}

	session, err := f.dc.Invite(ctx, recipient, []byte(offer), headers...)
	if err != nil {
		rtp.Close()
		return nil, fmt.Errorf("invite: %w", err)
	}
	if err := session.WaitAnswer(ctx, sipgo.AnswerOptions{
		Username: f.cfg.AuthUser, Password: f.cfg.AuthPass,
	}); err != nil {
		rtp.Close()
		_ = session.Bye(ctx)
		return nil, fmt.Errorf("wait answer: %w", err)
	}
	answerSDP := string(session.InviteResponse.Body())
	// Log FS's negotiated media line + codecs so we can see the agreed PT
	// (a PT mismatch vs the browser's RTP would make FS drop our audio).
	for _, line := range strings.Split(answerSDP, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "m=audio") || strings.HasPrefix(line, "a=rtpmap") {
			f.log.Printf("gateway: FS answer: %s", line)
		}
	}
	fsAddr, opusPT, dtmfPT, err := parseSDPMedia(answerSDP)
	if err != nil {
		rtp.Close()
		_ = session.Bye(ctx)
		return nil, fmt.Errorf("parse answer sdp: %w", err)
	}
	if err := session.Ack(ctx); err != nil {
		rtp.Close()
		_ = session.Bye(ctx)
		return nil, fmt.Errorf("ack: %w", err)
	}
	return &fsLeg{RTP: rtp, FSRTPAddr: fsAddr, OpusPT: opusPT, DTMFPT: dtmfPT, session: session, log: f.log}, nil
}

func (l *fsLeg) Close() {
	l.closeOnce.Do(func() {
		// The UAC (visitor) leg owns a client dialog to BYE; the UAS (agent)
		// leg's SIP dialog is owned elsewhere, so session is nil here.
		if l.session != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			// A failure here means FS may keep the visitor channel alive (and so
			// the bridged agent leg lingers); the error says why (non-2xx /
			// timeout / unreachable Contact). Success is the common path — no log.
			if err := l.session.Bye(ctx); err != nil && l.log != nil {
				l.log.Printf("gateway: visitor leg BYE to FS failed: %v", err)
			}
		}
		_ = l.RTP.Close()
	})
}

// buildSDP describes an Opus + telephone-event (DTMF) audio stream on the given
// port. As a UAC offer the gateway picks the PTs (111/101); as a UAS answer it
// MUST echo the PTs FreeSWITCH offered, so they're parameters.
func buildSDP(ip string, port int, opusPT, dtmfPT uint8) string {
	return strings.Join([]string{
		"v=0",
		fmt.Sprintf("o=tring 0 0 IN IP4 %s", ip),
		"s=tring-gateway",
		fmt.Sprintf("c=IN IP4 %s", ip),
		"t=0 0",
		fmt.Sprintf("m=audio %d RTP/AVP %d %d", port, opusPT, dtmfPT),
		fmt.Sprintf("a=rtpmap:%d opus/48000/2", opusPT),
		fmt.Sprintf("a=rtpmap:%d telephone-event/8000", dtmfPT),
		fmt.Sprintf("a=fmtp:%d 0-16", dtmfPT),
		"a=ptime:20",
		"a=sendrecv",
		"",
	}, "\r\n")
}

// parseSDPMedia pulls the connection IP (c=), audio port (m=audio), and the
// Opus payload type (a=rtpmap:<PT> opus) out of an SDP answer so we know where
// to send RTP and which PT FS expects.
func parseSDPMedia(sdp string) (addr *net.UDPAddr, opusPT, dtmfPT uint8, err error) {
	var ip string
	var port int
	rtpmapPT := func(line string) (uint8, bool) {
		// a=rtpmap:<PT> <codec>/...  → PT
		if n, e := strconv.Atoi(strings.Fields(strings.TrimPrefix(line, "a=rtpmap:"))[0]); e == nil {
			return uint8(n), true
		}
		return 0, false
	}
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		low := strings.ToLower(line)
		switch {
		case strings.HasPrefix(line, "c=IN IP4 "):
			ip = strings.TrimSpace(strings.TrimPrefix(line, "c=IN IP4 "))
		case strings.HasPrefix(line, "m=audio "):
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				port, _ = strconv.Atoi(fields[1])
			}
		case strings.HasPrefix(line, "a=rtpmap:") && strings.Contains(low, "opus"):
			if pt, ok := rtpmapPT(line); ok {
				opusPT = pt
			}
		case strings.HasPrefix(line, "a=rtpmap:") && strings.Contains(low, "telephone-event"):
			if pt, ok := rtpmapPT(line); ok {
				dtmfPT = pt
			}
		}
	}
	if ip == "" || port == 0 {
		return nil, 0, 0, fmt.Errorf("no audio media in answer")
	}
	if opusPT == 0 {
		opusPT = 111 // fall back to our offered PTs
	}
	if dtmfPT == 0 {
		dtmfPT = 101
	}
	return &net.UDPAddr{IP: net.ParseIP(ip), Port: port}, opusPT, dtmfPT, nil
}
