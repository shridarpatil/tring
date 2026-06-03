# Edge deployment — what's public, what's private

Since the ADR-001 cutover, all browser calling goes through the **media gateway**
(`cmd/gateway`) over raw WebRTC. The gateway is a B2BUA: a *public* face toward
browsers and a *private* face toward a FreeSWITCH that is never internet-exposed.
This doc is the network topology + the public/private split for production.

See `docs/ADR-001-webrtc-gateway.md` for the rationale and `docs/WEB_CALL_FLOW.md`
for the per-call sequence.

## Topology

```
                     PUBLIC (internet)                  │   PRIVATE (VPC / internal subnet)
                                                        │
   ┌─────────┐   WebRTC media (DTLS-SRTP/Opus, UDP)     │
   │ Browser │◀──────────── media ───────────┐         │
   │ visitor │   HTTPS  /webrtc/offer          │        │
   │ / agent │   WSS    /agent/ws              ▼        │
   └────┬────┘                          ┌────────────┐  plain RTP + SIP   ┌────────────┐
        │  relay (symmetric NAT)        │  GATEWAY   │───────────────────▶│ FreeSWITCH │
        ▼                               │ public face│  SIP :5090 + RTP   │  (PRIVATE) │
   ┌─────────┐                          │ + priv face│                    └─────┬──────┘
   │  TURN   │◀─────── relayed media ───│            │                          │ ESL
   │ (coturn)│                          └────────────┘                   ┌──────┴───────┐
   └─────────┘                                                           │ orchestrator │
                                                                         └──────┬───────┘
   your backend ──server→server (API key)──▶ ┌─────┐ ┌──────────┐ ┌───────┐    │
   (mints tokens)                             │ API │ │ Postgres │ │ Redis │◀───┘
   operators ──VPN/SSO──▶ /admin              └─────┘ └──────────┘ └───────┘   all PRIVATE
```

## Public vs private

| Component | Net | Ports | Why |
|---|---|---|---|
| **Gateway — browser face** | **PUBLIC** | `8087` (HTTPS `/webrtc/offer` + WSS `/agent/ws`), **UDP RTP range** on the public IP | The call edge. Browsers do WebRTC here. TLS-front the HTTP/WS (443); the UDP media range must be reachable at `TRING_PUBLIC_IP`. |
| **TURN** (coturn) | **PUBLIC** | `3478` (UDP/TCP), `5349` (TLS), relay UDP range | Relays media for clients behind symmetric NAT/CGNAT where direct ICE fails. |
| **SDK bundles** `/sdk/*.js` | **PUBLIC** via CDN/proxy | — | Static JS the browser loads. Serve from a CDN or reverse-proxy — does **not** require exposing the API. |
| **FreeSWITCH** | **PRIVATE** | SIP `5060`, RTP, ESL `8021`, WS `5066`/`7443` | The whole point: never internet-reachable. |
| **Gateway — FS face** | **PRIVATE** | SIP `5090` + RTP to FS | The B2BUA's internal leg; same subnet as FS. |
| **Orchestrator** | **PRIVATE** | ESL `8084`, IVR `8086` | Talks ESL to FS + DB. |
| **API** | **PRIVATE** (recommended) | `8085` | Token minting is server-to-server from your backend; `/fs/*` is FS↔API on the private net; `/admin` for operators over VPN/SSO. Browsers don't need it directly (below). |
| **Postgres / Redis** | **PRIVATE** | `5432`/`5434`, `6379` | Never exposed. |

## Why the API can stay private

The browser only needs three things, none requiring a public API:

1. **SDK JS** → CDN / reverse-proxy.
2. **A token** → minted by *your backend* (which holds the tenant API key, `tk_live_…`) and handed to the browser via `data-token-url` (agent) or `data-credentials-url` (trusted visitor). That backend→API call is server-to-server on the private network.
3. **Anonymous "call us"** → the SDK goes straight to the gateway with `data-gateway-url` + `data-flow-id` (no token; `gateway_require_auth=false`). No API call at all — the call is flow-pinned and can never reach a trunk.

If you want Tring's **public anonymous mint** instead (it adds an Origin allowlist + per-IP rate limit), expose only that one route — `POST /v1/sdk/guest-credentials` — publicly and keep the rest of the API private.

## Two faces of the gateway

The gateway binds two roles; don't conflate their IPs:

- `TRING_PUBLIC_IP` — the gateway's **public** IP, advertised to browsers as the WebRTC host candidate.
- `TRING_GATEWAY_ADVERT_IP` — the gateway's **private** IP, used for the SIP/RTP leg to FreeSWITCH (must be FS-reachable; it's also the Via/Contact host for FS's digest realm).
- `TRING_FS_SIP_ADDR` — FreeSWITCH's private SIP address (FS binds its LAN IP, not loopback).

## TURN

- Put coturn on its own **public IP**; open `3478` (UDP/TCP) + `5349` (TLS) + the relay UDP range.
- Use **short-lived TURN REST credentials** (coturn `use-auth-secret` / time-limited), not a static username/password shipped to browsers — mint them alongside the call token.
- `TRING_TURN_URL` / `_USERNAME` / `_PASSWORD` are returned to browsers in the ICE list **and** used by the gateway for its own candidates.
- `TRING_TURN_RELAY_ONLY=true` forces all media through TURN (hides peer IPs) at the cost of bandwidth — only if you need it.

## Production hardening checklist

- **TLS on every public edge**: gateway signaling behind 443 (HTTPS + WSS), TURN on 5349, admin behind TLS + SSO/VPN.
- **Auth**: the agent WebSocket (`/agent/ws`) always validates the agent JWT. Decide `gateway_require_auth` for `/webrtc/offer` — off for public "call us", **on** (require a minted gateway call token) to forbid anonymous visitors.
- **Scale the gateway horizontally** (its own boxes behind the LB), separate from FreeSWITCH — that's the tier that grows with concurrent calls. FS scales vertically per instance.
- **UDP port range**: widen `TRING_GATEWAY_UDP_MIN/MAX` (default `10000–10100` caps you at ~100 concurrent calls) and the matching FS RTP range; open them on the gateway's public IP.
- **Firewall**: lock the gateway's `:5090`/RTP-to-FS leg and all of FreeSWITCH / Postgres / Redis to the private subnet (security groups). Only the gateway's browser face + TURN + (optionally) the CDN/one mint route are public.

## Capacity (rule of thumb)

On a co-located 4-core/8GB box with Opus passthrough (no transcoding): ~150–300
concurrent web calls, CPU-bound on the gateway's SRTP + per-packet relay (FS
pass-through bridging is cheap). Transcoding (recording, codec mismatch) or
self-hosted TURN on the same box cuts that sharply. Load-test before committing
to a number, and plan production at ~60% of the measured ceiling. Grow by adding
gateway instances, not cores.
