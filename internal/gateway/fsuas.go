package gateway

// fsuas.go — the gateway as a SIP UAS toward FreeSWITCH (ADR-001 M4b/2).
//
// In Option B, FreeSWITCH delivers an agent call leg to the gateway as a SIP
// endpoint (sofia/gateway/tring-gw/<sip>, with the tenant in an X-Tring-Tenant
// header). FS is the offerer; the gateway answers. On INVITE the gateway:
//   1. resolves the target agent's live WebSocket (agent.go presence),
//   2. rings the browser and waits for it to accept (its WebRTC offer),
//   3. bridges the browser's WebRTC to a plain-RTP leg toward FS (reusing the
//      same relay as the visitor path), and
//   4. answers FS 200 OK with the gateway's RTP SDP.
// No agent SIP credentials ever touch the gateway.

import (
	"context"
	"net"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// defaultAgentRingTimeout applies when Config.AgentRingTimeout is unset.
const defaultAgentRingTimeout = 45 * time.Second

// ringTimeout returns the configured agent ring timeout, or the default.
func (g *Gateway) ringTimeout() time.Duration {
	if g.cfg.AgentRingTimeout > 0 {
		return g.cfg.AgentRingTimeout
	}
	return defaultAgentRingTimeout
}

// SIP headers the originate sets on an FS→gateway agent INVITE.
const (
	// tenantHeader disambiguates agents whose SIP usernames collide across tenants.
	tenantHeader = "X-Tring-Tenant"
	// agentLabelHeader is the non-PII string shown to the agent for this call
	// (e.g. a ticket/account ref). The backend sets it on a click-to-call so the
	// agent NEVER receives the dialed customer number. Absent ⇒ generic.
	agentLabelHeader = "X-Tring-Agent-Label"
	// autoAnswerHeader=1 tells the gateway this is a backend click-to-call the
	// agent already initiated, so the browser auto-answers instead of ringing.
	autoAnswerHeader = "X-Tring-Auto-Answer"
)

// registerFSUAS wires the inbound INVITE/ACK/BYE handlers onto the FS SIP
// server. Must be called before fsClient.serve.
func (g *Gateway) registerFSUAS() {
	f := g.fs
	f.srv.OnInvite(g.handleFSInvite)
	f.srv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) { _ = f.ds.ReadAck(req, tx) })
	// A BYE can be for a UAS dialog (agent leg, FS→gateway) or a UAC dialog
	// (visitor leg, gateway→FS). Try the server cache first, then the client
	// cache. Routing the visitor BYE to the client dialog is what ends its
	// StateRead() stream → the Bridge tears down (see gateway.go).
	f.srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		if err := f.ds.ReadBye(req, tx); err != nil {
			_ = f.dc.ReadBye(req, tx)
		}
	})
}

// handleFSInvite bridges one FS-delivered agent leg to the agent's browser.
func (g *Gateway) handleFSInvite(req *sip.Request, tx sip.ServerTransaction) {
	sipUser := req.Recipient.User
	tenant := headerValue(req, tenantHeader)
	// What the agent sees: the explicit label (click-to-call sets it and never
	// the dialed number), falling back to the SIP caller-ID for inbound / IVR
	// transfers (where showing the caller is expected, not a privacy leak).
	from := headerValue(req, agentLabelHeader)
	if from == "" {
		from = callerFrom(req)
	}
	auto := headerValue(req, autoAnswerHeader) == "1"

	dlg, err := g.fs.ds.ReadInvite(req, tx)
	if err != nil {
		g.log.Error("uas read invite failed", "err", err)
		return
	}

	key := agentKey{tenant: tenant, sip: sipUser}

	// FS is the offerer; learn its RTP endpoint + the PTs we must echo.
	fsAddr, opusPT, dtmfPT, err := parseSDPMedia(string(req.Body()))
	if err != nil {
		g.log.Error("agent leg bad sdp from fs", "err", err)
		_ = dlg.Respond(sip.StatusNotAcceptableHere, "Bad SDP", nil)
		return
	}

	// Ring ALL of the agent's tabs/devices and wait for the FIRST to accept; the
	// rest get "answered elsewhere". Declined / no live session / timeout fail
	// the leg so FS rolls on.
	_ = dlg.Respond(sip.StatusRinging, "Ringing", nil)
	ic := g.ringAgent(key, from, auto)
	if ic == nil {
		g.log.Info("agent offline, replying 480", "tenant", tenant, "sip", sipUser)
		_ = dlg.Respond(sip.StatusTemporarilyUnavailable, "Agent Offline", nil)
		return
	}

	var sess *agentSession
	var browserOffer string
	select {
	case of := <-ic.offer:
		sess, browserOffer = of.sess, of.sdp
		ic.cancelOthers(sess) // stop ringing the other tabs
	case <-ic.closed:
		ic.cancelAll("declined")
		_ = dlg.Respond(sip.StatusBusyHere, "Declined", nil)
		return
	case <-time.After(g.ringTimeout()):
		ic.cancelAll("no answer")
		_ = dlg.Respond(sip.StatusRequestTimeout, "No Answer", nil)
		return
	}

	// Bind our RTP socket for the FS leg and build the browser PeerConnection.
	rtp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(g.fs.cfg.AdvertIP), Port: 0})
	if err != nil {
		g.failAgentLeg(dlg, sess, ic.id, "rtp socket", err)
		return
	}
	localPort := rtp.LocalAddr().(*net.UDPAddr).Port
	// session is nil: the UAS dialog (dlg) owns SIP teardown, not fsLeg.Close.
	leg := &fsLeg{RTP: rtp, FSRTPAddr: fsAddr, OpusPT: opusPT, DTMFPT: dtmfPT}

	pc, toBrowser, err := g.newBridgePC()
	if err != nil {
		rtp.Close()
		g.failAgentLeg(dlg, sess, ic.id, "peer connection", err)
		return
	}

	callID := ic.id
	// agentMediaTimeout backstop: if FS stops relaying RTP (the visitor hung up
	// and the bridge ended) we tear down even if no clean SIP BYE reaches us, so
	// notifyEnded fires and the agent's screen closes.
	teardown := g.relay(pc, toBrowser, leg, agentMediaTimeout, func() {
		g.agents.leaveCall(key) // agent back to "online" (available to ring)
		sess.finishIncoming(callID)
		sess.notifyEnded(callID, "ended")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = dlg.Bye(ctx)
	})
	// Let a browser-side hangup (WS "hangup") tear down this live bridge — the
	// relay's pc-state watcher only catches a slow ICE failure, so without this
	// the FS leg would linger after the agent hangs up. Idempotent via once.
	ic.setTeardown(teardown)
	// The agent is committed to this call now — mark them "on_call" so the
	// orchestrator's ring-all stops routing new calls here. Balanced with the
	// leaveCall in the teardown above (both tied to this relay's lifecycle).
	g.agents.enterCall(key)

	// Negotiate the browser leg (their offer → our answer) and hand the answer
	// back over the WebSocket.
	answerSDP, err := g.negotiateAnswer(pc, browserOffer)
	if err != nil {
		g.log.Error("agent leg negotiate failed", "err", err)
		teardown()
		_ = dlg.Respond(sip.StatusInternalServerError, "Negotiate", nil)
		return
	}
	if err := sess.sendAnswer(callID, answerSDP); err != nil {
		g.log.Error("agent leg send answer failed", "err", err)
		teardown()
		_ = dlg.Respond(sip.StatusInternalServerError, "Signal", nil)
		return
	}

	// Answer FS with our RTP SDP (echoing FS's PTs). Media flows both ways now.
	if err := dlg.RespondSDP([]byte(buildSDP(g.fs.cfg.AdvertIP, localPort, opusPT, dtmfPT))); err != nil {
		g.log.Error("agent leg 200 ok failed", "err", err)
		teardown()
		return
	}
	g.log.Info("agent bridge up", "tenant", tenant, "sip", sipUser, "fs_rtp", fsAddr)

	// Tear down when FS ends the dialog (BYE).
	go func() {
		for state := range dlg.StateRead() {
			if state == sip.DialogStateEnded {
				teardown()
				return
			}
		}
	}()
}

// failAgentLeg logs, 500s the SIP leg, and notifies the browser that media
// setup failed (the browser was already ringing/answered).
func (g *Gateway) failAgentLeg(dlg *sipgo.DialogServerSession, sess *agentSession, callID, what string, err error) {
	g.log.Error("agent leg failed", "stage", what, "err", err)
	sess.finishIncoming(callID)
	sess.notifyEnded(callID, "media setup failed")
	_ = dlg.Respond(sip.StatusInternalServerError, "Media", nil)
}

// headerValue returns a request header's value, or "" if absent.
func headerValue(req *sip.Request, name string) string {
	if h := req.GetHeader(name); h != nil {
		return h.Value()
	}
	return ""
}

// callerFrom extracts a human-ish caller label from the From header.
func callerFrom(req *sip.Request) string {
	f := req.From()
	if f == nil {
		return ""
	}
	if f.DisplayName != "" {
		return f.DisplayName
	}
	return f.Address.User
}
