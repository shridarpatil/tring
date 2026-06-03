# ADR-001: pion WebRTC gateway in front of a private FreeSWITCH

- **Status:** Proposed (gated on the bridge POC — see `docs/POC_PION_FS_BRIDGE.md`)
- **Date:** 2026-06-03
- **Deciders:** Tring core
- **Related:** `docs/WEB_CALL_FLOW.md`, `docs/EDGE_DEPLOYMENT.md`

## Context

Tring's browser calling today is **SIP-over-WebSocket**: the SDK (sip.js) registers
to FreeSWITCH over WSS and media flows browser↔FreeSWITCH directly. Keeping
FreeSWITCH private with that model still requires exposing a fair bit at the edge
(see `EDGE_DEPLOYMENT.md`): a public WSS reverse-proxy into FS `:5066`, a
browser-reachable RTP path (FS `ext-rtp-ip` + forwarded port range), and TURN.
That means **FreeSWITCH's SIP stack and RTP are internet-reachable** (even if
proxied), and **browsers hold SIP credentials** and register to FS. SIP is the
#1 VoIP abuse target (registration scanning, toll fraud, parser CVEs).

We want a posture where **FreeSWITCH and the Tring API never sit on the public
internet** — only a thin, hardened edge does. We studied the WhatsApp Business
Calling API and the sibling Whatomate app (`~/workspace/whatomate`,
`internal/calling/*`): it uses a **pion (Go) WebRTC gateway** that terminates
browser/Meta WebRTC and bridges RTP — no FreeSWITCH, no SIP, no WSS exposed.
That model is clean because **Meta is the trusted edge** (Whatomate only peers
with Meta). Tring's web-calling callers are untrusted browsers on the open
internet, so *we* are the edge — but the gateway pattern still applies.

## Decision

Adopt a **pion-based WebRTC media gateway** as the browser-facing front door for
web calling:

```
Browser ──WebRTC (DTLS-SRTP/Opus), HTTPS signaling, app auth──► Tring pion gateway (public)
                                                                 │  bridges media
                                                                 ▼
                                                        FreeSWITCH (PRIVATE) ──► IVR / trunks / agents
```

- The browser peers with **our gateway**, not FreeSWITCH. Signaling is plain
  HTTPS (SDP offer → answer), authenticated by the app's existing credentials
  (agent JWT / tenant API key / guest creds).
- The gateway terminates WebRTC and **bridges media into a private FreeSWITCH**,
  which keeps doing everything it does today (IVR executor, trunk bridging,
  masking, agent bridging) as a private SIP/RTP endpoint.
- The current SIP-over-WSS path is **deprecated for the public surface** but
  kept as a fallback until the gateway is proven (see POC). FreeSWITCH's public
  WS bind is removed once the gateway ships.

This decision is **contingent on the bridge POC** answering the one unproven
question (can a Go gateway cleanly bridge browser WebRTC into a FreeSWITCH call,
incl. DTMF + teardown). If the POC fails, we fall back to the hardened
SIP/WSS edge in `EDGE_DEPLOYMENT.md`.

## Rationale (why pion gateway over SIP/WSS)

Security — the deciding factor:
1. **FreeSWITCH's SIP stack never faces the internet** — the largest VoIP
   attack surface (scanning, toll fraud, SIP CVEs) becomes unreachable.
2. **No SIP credentials in the browser, no registration to hijack.** Auth is
   the app's JWT / API key (now revocable — see API-key management), unified and
   auditable.
3. **HTTPS signaling is CDN/WAF/bot-challenge/rate-limit-able**, and we reject
   junk *before* allocating any media (RTCPeerConnection / UDP ports).
4. **One memory-safe Go choke point** we fully control — per-tenant authz and
   concurrency caps enforced before a single RTP packet.
5. **Smaller public surface:** gateway + TURN, vs (WSS proxy + FS SIP + FS RTP +
   TURN).

We keep FreeSWITCH's value (trunking, IVR, masking) — we just stop exposing it.

## Consequences

**Positive**
- FreeSWITCH + Tring API can be fully private; only the gateway + TURN are public.
- Unified, revocable auth at the app layer; no SIP credential distribution.
- Application-layer DDoS/abuse is gated in Go before media (see "DDoS" below).
- FS config simplifies: no public WS bind, no `ext-rtp-ip` forwarding for browsers.

**Negative / costs**
- **We own a media bridge** (pion↔FreeSWITCH): codecs, DTMF, jitter, packet loss,
  recording, reconnection. We trade FreeSWITCH's battle-tested C media path for
  our own Go code (smaller surface, safer language — but our correctness now).
- **The gateway is a media tier**: every call's RTP flows through it (~2× internal
  bandwidth: browser-leg + FS-leg), CPU for DTLS-SRTP + relay. Scale it
  horizontally; it's a real new component to run.
- **Two media tiers** (gateway + FS) instead of one → more hops/latency than
  browser↔FS direct.
- **Volumetric L3/L4 DDoS still needs network-layer defense** (upstream scrubbing,
  hardened coturn). pion only handles the app/exposure layer.

**Neutral**
- The orchestrator/ESL control plane is unchanged; FS still owns call state.
- TURN is still required for the browser↔gateway leg behind symmetric NAT.
- Whatomate's `internal/calling` is a **reference for the browser-side pion
  termination + RTP relay**, but **not** for the FS bridge — Whatomate bridges
  pion↔Meta (both WebRTC); our novel part is the pion↔FreeSWITCH (RTP/SIP) leg.

## DDoS posture (summary)

The gateway strongly improves *application-layer* DDoS: auth-before-media,
HTTPS WAF/CDN, per-tenant caps, ICE/PC timeouts, single-UDP-port mux
(`SetICEUDPMux`) or a tight port range, optional `RelayOnly`. It does **not**
stop volumetric UDP/L3-L4 floods — those need upstream scrubbing + a hardened
coturn. No media edge escapes that last part.

## Alternatives considered

1. **Harden the SIP/WSS edge (status quo + hardening).** Config-only (proxy +
   coturn + FS ext-ip), and it *scales best* — media goes browser↔FS directly,
   the app stays control-plane. But FS SIP + RTP remain internet-reachable and
   browsers keep SIP creds. Rejected as the long-term posture; kept as the
   fallback and the interim shipping option.
2. **pion WebRTC gateway (this ADR).** Best security; new media component.
3. **Channel via WhatsApp/Meta (Whatomate model).** Meta becomes the edge — best
   posture of all, but only serves the WhatsApp channel, not open web / PSTN.
   Complementary, not a replacement.

## Sequencing

Ship the **hardened SIP/WSS edge now** (it's config on top of what Tring already
does — fastest path to a private-FS deployment). Treat the **pion gateway** as
the v2 public architecture, gated on the POC below. Do **not** remove the
SIP/WSS path until the bridge POC is proven.
