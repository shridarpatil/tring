# POC: pion ↔ FreeSWITCH media bridge

Validates the one unproven piece of `ADR-001` before we commit to the pion
WebRTC gateway. Everything else in that architecture (browser WebRTC, HTTPS
signaling, ICE/TURN, app auth) is well-trodden; the risk is entirely:

> **Can a Go (pion) gateway terminate a browser's Opus WebRTC leg and bridge it
> bidirectionally into a FreeSWITCH call — with two-way audio, working DTMF, and
> clean teardown — while FreeSWITCH is never exposed to the browser?**

If yes → ADR-001 is GO. If the bridge is too lossy/laggy or DTMF/teardown can't
be made reliable → fall back to the hardened SIP/WSS edge.

## Why this is the risk (and Whatomate isn't a full reference)

Whatomate's `internal/calling` bridges **pion ↔ Meta** — *both legs are WebRTC*,
so it just forwards RTP between two pion PeerConnections (`bridge.go`). Tring's
gateway must bridge **pion (browser) ↔ FreeSWITCH (plain RTP/SIP)** — the FS leg
is *not* WebRTC. That FS-side leg is the novel, unproven part. Reuse Whatomate's
browser-side termination + relay patterns; build and prove the FS leg here.

## In scope

A throwaway vertical slice in `poc/pion-gateway/` (not wired into the app):
- a Go gateway (`github.com/pion/webrtc/v4` — same major as Whatomate) with one
  HTTP endpoint that accepts an SDP offer and returns an answer;
- a static test page (`getUserMedia` → `RTCPeerConnection` → POST offer → play
  remote audio);
- bridging that gateway's WebRTC leg to the **existing dockerized FreeSWITCH**.

## Out of scope (explicitly)

Multi-tenant, auth, scale/load, recording, reconnection/ICE-restart, TURN
(use host networking / localhost), agent transfer, the admin UI, production
error handling. Those only matter *after* the bridge is proven.

## The key design choice to evaluate: the FS leg

How the gateway attaches to FreeSWITCH is what the POC must pick:

- **Primary candidate — gateway as a SIP B2BUA to FS over plain UDP SIP/RTP**
  (private network). pion handles the browser side (DTLS-SRTP/Opus); the gateway
  re-originates a *plain* RTP/SIP leg into FS. Prefer **Opus passthrough** on the
  FS leg to avoid transcoding; FS transcodes to G711 only on the trunk side if
  needed. Needs a minimal SIP client in the gateway (a Go SIP lib, or hand-rolled
  UDP SIP for the POC).
- **Fallback — RTP via ESL-orchestrated leg**: use ESL to `originate` a leg
  (e.g. into the IVR/`echo`) and exchange raw RTP with the gateway via a
  pre-agreed SDP. Avoids a SIP stack but is more bespoke.

Pick one in milestone 2; document why.

## Milestones (each is a go/no-go checkpoint)

1. **Browser ↔ gateway WebRTC echo.** Gateway loops the browser's audio back.
   Proves pion termination + Opus + signaling. (No FS yet.)
2. **Gateway ↔ FS echo.** Gateway bridges its WebRTC leg to a FreeSWITCH `echo`
   app; the browser hears FS echo. **Proves the bridge mechanism** (the crux).
3. **Gateway → FS IVR.** Point the FS leg at the existing IVR executor: browser
   hears the greeting, **presses DTMF** (relayed across WebRTC↔RTP↔FS), reaches a
   transfer/menu. Proves DTMF + a real flow.
4. **(stretch) Gateway → FS → trunk.** Bridge through to the SIP simulator
   (`sim/`, `127.0.0.1:5070`). Proves the full browser→gateway→FS→trunk path.

## Success criteria

- Two-way audio with acceptable quality (no persistent clipping/robotic audio;
  informal MOS is fine for a POC).
- **DTMF works end-to-end** (RFC 2833 / `telephone-event` across the bridge).
- **Clean teardown** in both directions (browser hangs up → FS leg ends; FS/IVR
  hangs up → browser PC closes). No leaked PCs / RTP goroutines.
- Added latency is reasonable (target < ~150 ms over browser↔FS-direct).
- FreeSWITCH is reached **only** by the gateway — never by the browser.

## Risks to retire (what the POC is really testing)

- **Codec/transcoding:** Opus on the browser leg vs G711 on trunks. Where does
  transcode happen (FS leg in Opus + FS transcodes downstream, vs gateway sends
  PCMU)? Cost/quality.
- **DTMF path:** `telephone-event` negotiation and relay across the WebRTC↔RTP
  boundary, into FS's DTMF handling (the IVR `play_and_get_digits`).
- **Bridge mechanism:** SIP B2BUA vs RTP-via-ESL — reliability + complexity.
- **Jitter/packet loss** in the Go RTP relay (buffering, reordering).

## Deliverables

- `poc/pion-gateway/` — gateway `main.go` + `index.html` (throwaway).
- A short **findings note** (append to this file): which FS-leg mechanism, codec
  decision, DTMF result, latency, and a **GO / NO-GO** recommendation for ADR-001.

## Rough effort

~1–2 focused weeks for one engineer to reach milestone 3 (the decision point).
Milestone 4 is a stretch.

## Environment

- Existing `make up` FreeSWITCH (private), the `sim/` SIP simulator for the trunk
  leg, `pion/webrtc/v4`. Reference (browser-side patterns only):
  `~/workspace/whatomate/internal/calling/{webrtc.go,bridge.go,outgoing.go}`.
