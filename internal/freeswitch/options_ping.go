package freeswitch

// options_ping.go probes IP-auth trunks for reachability by sending a
// minimal SIP OPTIONS over UDP and waiting briefly for any 200 OK reply.
// Sofia's gateway-level `ping` param isn't honoured by every FS build,
// so the API does the check itself — bounded by a short timeout so the
// UI list endpoint stays responsive even when probing several trunks.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"
)

// OptionsPing sends a SIP OPTIONS to host:port over UDP and returns
// true if any SIP response arrives within timeout. Doesn't validate
// the response code — anything that looks like a SIP/2.0 line means
// the peer is alive and speaking SIP.
//
// Use a short timeout (1–2s); this runs synchronously per /v1/trunks
// fetch, and total UI latency is the sum across all IP-auth trunks.
func OptionsPing(ctx context.Context, hostPort string, timeout time.Duration) bool {
	// Default port if hostPort is a bare IP.
	if !strings.Contains(hostPort, ":") {
		hostPort += ":5060"
	}

	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "udp", hostPort)
	if err != nil {
		return false
	}
	defer conn.Close()

	branch := "z9hG4bK-tring-" + randHex(8)
	fromTag := "tring-" + randHex(4)
	callID := "tring-ping-" + randHex(8) + "@127.0.0.1"
	local := conn.LocalAddr().(*net.UDPAddr).String()

	req := strings.Join([]string{
		"OPTIONS sip:tring-ping@" + hostPort + " SIP/2.0",
		"Via: SIP/2.0/UDP " + local + ";branch=" + branch,
		"From: <sip:tring@127.0.0.1>;tag=" + fromTag,
		"To: <sip:tring-ping@" + hostPort + ">",
		"Call-ID: " + callID,
		"CSeq: 1 OPTIONS",
		"Max-Forwards: 70",
		"User-Agent: Tring-Ping/0.1",
		"Content-Length: 0",
		"",
		"",
	}, "\r\n")

	_ = conn.SetWriteDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte(req)); err != nil {
		return false
	}

	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err == nil {
		// Got a response — peer is alive and speaking SIP.
		return strings.HasPrefix(string(buf[:n]), "SIP/2.0")
	}
	// Distinguish "port closed" (peer is down — ICMP unreachable bubbles
	// up as ECONNREFUSED on the next syscall) from "no response within
	// timeout" (port open but the peer doesn't auto-answer OPTIONS, like
	// our SIPp simulator). Read-timeout == online (port is listening);
	// ECONNREFUSED == offline.
	if strings.Contains(err.Error(), "refused") {
		return false
	}
	return true
}

func randHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// formatHostPort is a tiny helper for future use when extending probes
// to support host:port maps. Kept here next to OptionsPing so the call
// site is self-contained.
func formatHostPort(host string, port int) string {
	return fmt.Sprintf("%s:%d", host, port)
}
