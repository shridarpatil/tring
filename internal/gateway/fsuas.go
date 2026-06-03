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

// tenantHeader carries the tenant id on an FS→gateway agent INVITE so the
// gateway can disambiguate agents whose SIP usernames collide across tenants.
const tenantHeader = "X-Tring-Tenant"

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
	from := callerFrom(req)

	dlg, err := g.fs.ds.ReadInvite(req, tx)
	if err != nil {
		g.log.Printf("gateway: UAS read invite failed: %v", err)
		return
	}

	sess, ok := g.agents.lookup(agentKey{tenant: tenant, sip: sipUser})
	if !ok {
		g.log.Printf("gateway: agent %s/%s offline → 480", tenant, sipUser)
		_ = dlg.Respond(sip.StatusTemporarilyUnavailable, "Agent Offline", nil)
		return
	}

	// FS is the offerer; learn its RTP endpoint + the PTs we must echo.
	fsAddr, opusPT, dtmfPT, err := parseSDPMedia(string(req.Body()))
	if err != nil {
		g.log.Printf("gateway: agent leg bad SDP from FS: %v", err)
		_ = dlg.Respond(sip.StatusNotAcceptableHere, "Bad SDP", nil)
		return
	}

	// Ring the browser and wait for it to accept (offer), decline, or time out.
	_ = dlg.Respond(sip.StatusRinging, "Ringing", nil)
	ic := sess.startIncoming(from)

	var browserOffer string
	select {
	case browserOffer = <-ic.offer:
		// accepted
	case <-ic.closed:
		sess.finishIncoming(ic.id)
		_ = dlg.Respond(sip.StatusBusyHere, "Declined", nil)
		return
	case <-time.After(g.ringTimeout()):
		sess.finishIncoming(ic.id)
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
	teardown := g.relay(pc, toBrowser, leg, func() {
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

	// Negotiate the browser leg (their offer → our answer) and hand the answer
	// back over the WebSocket.
	answerSDP, err := g.negotiateAnswer(pc, browserOffer)
	if err != nil {
		g.log.Printf("gateway: agent leg negotiate failed: %v", err)
		teardown()
		_ = dlg.Respond(sip.StatusInternalServerError, "Negotiate", nil)
		return
	}
	if err := sess.sendAnswer(callID, answerSDP); err != nil {
		g.log.Printf("gateway: agent leg send answer failed: %v", err)
		teardown()
		_ = dlg.Respond(sip.StatusInternalServerError, "Signal", nil)
		return
	}

	// Answer FS with our RTP SDP (echoing FS's PTs). Media flows both ways now.
	if err := dlg.RespondSDP([]byte(buildSDP(g.fs.cfg.AdvertIP, localPort, opusPT, dtmfPT))); err != nil {
		g.log.Printf("gateway: agent leg 200 OK failed: %v", err)
		teardown()
		return
	}
	g.log.Printf("gateway: agent bridge up %s/%s (FS RTP %s)", tenant, sipUser, fsAddr)

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
	g.log.Printf("gateway: agent leg %s failed: %v", what, err)
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
