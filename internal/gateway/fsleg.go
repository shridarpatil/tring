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
// reused for every call leg. Built once in gateway.New when FS is configured.
type fsClient struct {
	cfg FSConfig
	dc  *sipgo.DialogClientCache
	log *log.Logger
}

func newFSClient(ctx context.Context, cfg FSConfig, logger *log.Logger) (*fsClient, error) {
	// The UA hostname drives the From header domain (defaults to "localhost").
	// FreeSWITCH's auto_from challenge realm derives from it, so it MUST be the
	// directory domain where the auth user lives, or FS looks up the user under
	// the wrong domain and 403s ("Can't find user [1000@localhost]").
	ua, err := sipgo.NewUA(sipgo.WithUserAgentHostname(cfg.AdvertIP))
	if err != nil {
		return nil, err
	}
	bind := fmt.Sprintf("%s:%d", cfg.AdvertIP, cfg.SIPPort)
	client, err := sipgo.NewClient(ua, sipgo.WithClientHostname(cfg.AdvertIP), sipgo.WithClientAddr(bind))
	if err != nil {
		return nil, err
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		return nil, err
	}
	// Listen so responses + in-dialog requests (BYE from FS) are received.
	go func() { _ = srv.ListenAndServe(ctx, "udp", bind) }()

	// From/Contact user must be the directory user we digest-auth as, or FS
	// rejects the INVITE (403) — it authenticates against the From user.
	contact := sip.ContactHeader{Address: sip.Uri{User: cfg.AuthUser, Host: cfg.AdvertIP, Port: cfg.SIPPort}}
	return &fsClient{cfg: cfg, dc: sipgo.NewDialogClientCache(client, contact), log: logger}, nil
}

// fsLeg is one established call into FreeSWITCH: a UDP RTP socket bound on the
// gateway, the negotiated FS RTP endpoint, and the SIP dialog.
type fsLeg struct {
	RTP       *net.UDPConn
	FSRTPAddr *net.UDPAddr
	OpusPT    uint8 // payload type FS negotiated for Opus
	DTMFPT    uint8 // payload type FS negotiated for telephone-event (DTMF)
	session   *sipgo.DialogClientSession
	closeOnce sync.Once
}

// dial INVITEs the configured target, authenticates, and returns the live leg
// with a bound RTP socket and FS's RTP address. Caller must Close it.
func (f *fsClient) dial(ctx context.Context) (*fsLeg, error) {
	// Bind the gateway RTP socket first so we can advertise its port in the SDP.
	rtp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(f.cfg.AdvertIP), Port: 0})
	if err != nil {
		return nil, fmt.Errorf("rtp socket: %w", err)
	}
	localPort := rtp.LocalAddr().(*net.UDPAddr).Port
	f.log.Printf("gateway: our RTP socket on %s:%d (advertised to FS)", f.cfg.AdvertIP, localPort)

	offer := buildSDP(f.cfg.AdvertIP, localPort)
	host, portStr, _ := net.SplitHostPort(f.cfg.SIPAddr)
	port, _ := strconv.Atoi(portStr)

	// Flow-pinned (web call): route to the gwflow extension and carry the flow
	// id in a SIP header — no DID. Otherwise dial the configured Target
	// (echo/DID). FreeSWITCH exposes X-… headers as ${sip_h_X-…}.
	dest := f.cfg.Target
	headers := []sip.Header{sip.NewHeader("Content-Type", "application/sdp")}
	if f.cfg.FlowID != "" {
		dest = gwFlowExtension
		headers = append(headers, sip.NewHeader("X-Tring-Flow-Id", f.cfg.FlowID))
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
	return &fsLeg{RTP: rtp, FSRTPAddr: fsAddr, OpusPT: opusPT, DTMFPT: dtmfPT, session: session}, nil
}

func (l *fsLeg) Close() {
	l.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = l.session.Bye(ctx)
		_ = l.RTP.Close()
	})
}

// buildSDP offers Opus (passthrough from the browser leg) + telephone-event DTMF.
func buildSDP(ip string, port int) string {
	return strings.Join([]string{
		"v=0",
		fmt.Sprintf("o=tring 0 0 IN IP4 %s", ip),
		"s=tring-gateway",
		fmt.Sprintf("c=IN IP4 %s", ip),
		"t=0 0",
		fmt.Sprintf("m=audio %d RTP/AVP 111 101", port),
		"a=rtpmap:111 opus/48000/2",
		"a=rtpmap:101 telephone-event/8000",
		"a=fmtp:101 0-16",
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
