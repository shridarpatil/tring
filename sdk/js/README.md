# @tring/voice

Drop-in browser voice SDK for the Tring CPaaS — WebRTC calling on any web page, no SIP/FreeSWITCH knowledge needed. Built into a few bundles, all of which the **Tring API serves itself at `/sdk/*`** (so self-hosters need no CDN):

| Bundle | Served at | For |
|---|---|---|
| **`voice.js`** (`dist/voice.iife.js`) | `/sdk/voice.js` | **Website visitors** — a "Call" button on any public page drops the caller into your IVR over WebRTC, no account. Anonymous (`data-gateway-url`+`data-flow-id`) or trusted (`data-credentials-url`). |
| **`agent.js`** (`dist/agent.iife.js`) | `/sdk/agent.js` | **CRM agents** — connects the gateway WebSocket and renders the in-call panel (ring/answer/hangup/DTMF). **No Tring API access — it cannot place calls;** origination is backend-only. Auth: short-lived agent **JWT**. |
| **`tring.js`** (`dist/tring.iife.js`) | `/sdk/tring.js` | Bare SDK — exposes `window.Tring` for programmatic use. |
| **`tring.esm.js`** | — (npm) | `import Tring from '@tring/voice'` for bundlers. |

> The widget is served from the same origin as your API, e.g. `https://your-tring-host/sdk/voice.js`. No `cdn.tring.io` dependency — point `src` at your own deployment.

> **Building for another platform (Flutter / iOS / Android)?** This is a thin
> client over a small, language-agnostic WebRTC protocol — see
> [**How it works — protocol reference**](#how-it-works--protocol-reference-port-the-sdk-to-any-platform)
> below. It's the full wire spec; the TypeScript is just the reference implementation.

## Quickstart — agent widget (`agent.js`)

Drop **one script tag** in your agent app. It connects the gateway WebSocket and
renders the in-call panel (ring → Accept/Decline / connected → hangup + DTMF).
It does **not** place calls — origination is backend-only (below).

```html
<script src="https://your-tring-host/sdk/agent.js"
        data-token-url="/tring/token"></script>
```

`/tring/token` is **your** endpoint that mints a fresh agent JWT server-side
(`GET /v1/agents/<id>/token`, using your tenant API key) for the logged-in agent.
The browser never sees the API key. That Tring endpoint returns
`{ token, gateway_url, ice_servers }`, so the widget learns where to connect from
the same fetch — your endpoint just passes the JSON through. (For a static token
on a server-rendered page, use `data-token="<jwt>"` **and** `data-gateway-url="…"`,
since there's no fetch to carry the gateway URL.)

### Placing an outbound call (backend-originated — the agent never sees the number)

The SDK has **no Tring API access**, so your own "Call" button hits **your**
backend, which originates via Tring and passes the number — the agent's browser
never receives it (mirrors the visitor flow). The agent leg auto-answers (they
already clicked) and the panel shows your `agent_label`.

```
agent clicks a customer row  →  your backend  →  POST /v1/calls
                                 (tenant API key)   { agent_id, to: "<number>", agent_label: "Ticket #4821" }
                              →  the gateway rings the agent's widget (auto-answers, label only),
                                 then dials the customer into the same call
```

```js
// your backend (holds the API key) — the browser sent a customer id, not a number
await fetch("https://your-tring-host/v1/calls", {
  method: "POST",
  headers: { Authorization: `Bearer ${TENANT_API_KEY}`, "Content-Type": "application/json" },
  body: JSON.stringify({ agent_id, to: customer.number, agent_label: customer.ref }),
});
```

Or let the SDK send that request: `Tring.startCall(url, data?)` POSTs to **your**
endpoint (same-origin cookies, `data` as a JSON body, `X-CSRF-Token` from
`<meta name="csrf-token">` if present). It rejects if the agent isn't connected,
a previous call request is still in flight, or your endpoint returns non-2xx.
Put IDs in it, never a number.

```html
<button onclick="Tring.startCall('/crm/call?id=4821', { ticket: 'T-99' })">Call</button>
```

`agent_label` is optional and the only thing the agent sees for the call (empty →
a generic "Incoming call"). Inbound calls / IVR transfers ring the same panel with
Accept/Decline. See `examples/demo-portal/` for a runnable backend (`/agent/originate`).

### Connection lifecycle & page navigation ⚠️

The agent's gateway connection (presence WebSocket + any active call's
`RTCPeerConnection`) lives in the **page's JavaScript context**. That has a hard
consequence integrators must design around:

| Navigation | Connection | Active call |
|---|---|---|
| **SPA / client-side routing** (Vue Router, React Router, …) | **persists** ✅ | **persists** ✅ |
| **Full page load / hard reload / link to a new URL** | **drops, then re-registers** | **lost** ❌ |

A full page navigation destroys the JS realm, so the browser tears down every
WebSocket, the PeerConnection, and the mic stream — there is no way to keep them.
On the new page the widget re-initializes and the agent **re-registers presence
automatically** (a brief offline blip; presence TTL is 30s), but **any call in
progress drops**. A page *refresh* mid-call drops the call for the same reason.

> The SDK auto-reconnects the presence socket across *transient* network drops
> (Wi-Fi blips, gateway redeploys, token expiry) **within a live page** — keepalive
> + backoff, re-minting the token each attempt. That does **not** cover page
> navigation, which is a different JS context entirely.

**To keep the agent online (and calls alive) across navigation, mount the widget
in a context that doesn't reload:**

1. **Best — an SPA shell.** Load `agent.js` once in your app shell; route changes
   are client-side, so the connection and any call survive.
2. **Multi-page / server-rendered CRM** (full reload per click): host the widget in
   a **persistent iframe or a popout window** that isn't reloaded as the main
   content navigates — the classic dialer-toolbar pattern.
3. Otherwise the agent disconnects/reconnects on every navigation: presence
   recovers, but they cannot hold a call across page changes.

(Persisting the session to survive reload isn't a fix: WebRTC **media** can't
cross a page load, so even reattaching signaling wouldn't restore audio.)

## Website visitor calling (`voice.js`)

Put voice on a public page so visitors call **into your IVR** — no phone number. Both anonymous guests and logged-in callers use the **same tag**, pointing at a backend endpoint that mints a gateway call token:

```html
<script src="https://your-tring-host/sdk/voice.js"
        data-credentials-url="/your/credentials"></script>
```

Your endpoint mints via `POST /v1/guest-credentials` (tenant API key) and returns `{ gateway_url, flow_id, token }`. The only difference between guest and logged-in is what it checks first and whether it passes a `client_ref`:
- **Logged-in** → verify the session → mint **with** `client_ref` (attributed on the CDR).
- **Anonymous guest** → gate with a **CAPTCHA / rate-limit** → mint **without** `client_ref`.

The flow is pinned **inside the token** (so it's never in the page), and the call can never reach a trunk. The visitor taps the floating "Call" button (or your own `<button data-tring-call>` with `data-render-button="false"`). See `examples/demo-portal/` — one runnable app with `/support` (guest), `/portal` (logged-in), and `/crm` (agent).

> **Default is token-gated** (`gateway_require_auth=true`): a token-less offer is rejected before any port/FS is allocated, so every visitor call needs a backend-minted token (`POST /v1/guest-credentials`). There is no public mint. To allow a raw, no-backend anonymous path instead (`data-gateway-url` + `data-flow-id`, token-less), set `gateway_require_auth=false` — still flow-pinned/can't-reach-a-trunk, but the offer endpoint is then unauthenticated.

## Programmatic use (bare SDK)

The browser needs a **short-lived JWT**, not the tenant API key. Your backend
mints it (`GET /v1/agents/<id>/token`) and ships it to the browser. The SDK is
**receive-only** — it surfaces call legs the backend originates; it has no
`call()` and no API client.

```html
<script src="https://your-tring-host/sdk/tring.js"></script>
<script>
  const tring = await Tring.init({
    token: 'eyJ...',                     // JWT from /v1/agents/<id>/token (5-min TTL)
    gatewayUrl: 'https://gw.tring.io',   // required — raw WebRTC to the media gateway
    // autoAnswer: false,                // gate TRUE inbound on a UI confirm (default true)
  });

  tring.on('registered',    ()       => console.log('online'));        // WS up
  tring.on('call:ringing',  ({call}) => console.log('ringing', call.to)); // call.to = label, not a number
  tring.on('call:answered', ({call}) => console.log('connected'));
  tring.on('call:ended',    ()       => console.log('ended'));

  // To END a call, use the Call from the ringing/answered event:
  //   call.accept(); call.hangup(); call.sendDTMF('1');
</script>
```

A backend click-to-call leg auto-answers (the agent already initiated). A TRUE
inbound (IVR transfer / inbound DID) rings per `autoAnswer` (default `true`; set
`false` to gate on Accept/Decline). `call.to` carries the backend-supplied label,
never the dialed number.

## Development

```bash
npm install
npm run build      # produces dist/tring.{iife,esm}.js + .map + .d.ts
npm run watch      # rebuild on change
npm run typecheck  # tsc --noEmit
```

## Smoke test in a real browser

The runnable end-to-end demo lives in [`examples/demo-portal/`](../../examples/demo-portal/)
(a small Flask app) — it exercises all three flows against a locally-running
stack: anonymous "call us", logged-in support, and the agent CRM. Have the Tring
API + orchestrator + gateway + FreeSWITCH up (root `docker compose up -d`, or
`go run ./cmd/api` + `./cmd/orchestrator` + `./cmd/gateway`), then `make demo`
and follow its README.

## Note: no browser WSS / cert dance

Since the ADR-001 cutover the browser peers with the **media gateway** over raw WebRTC (HTTP signaling on `:8087`) — there's no SIP-over-WSS to FreeSWITCH and no self-signed `:7443` cert to pre-accept. For local dev on one machine, point everything at loopback (`TRING_INTERNAL_IP=127.0.0.1`); for production, put TLS in front of the gateway (`https://` + WSS on `:8087`) and set `TRING_GATEWAY_PUBLIC_URL`. See `docs/src/content/docs/architecture/overview.md`.

## How it works — protocol reference (port the SDK to any platform)

This SDK is just a thin client over a small, language-agnostic protocol. Nothing
here is JavaScript-specific — a **Flutter, iOS, Android, or React-Native** SDK
implements the exact same wire contract against any WebRTC stack
(`flutter_webrtc`, `WebRTC.framework`, `libwebrtc`, …). Read this section as the
spec; the TypeScript files are just the reference implementation.

### The big picture

```
   client (browser / mobile)            PUBLIC EDGE                 PRIVATE
   ┌─────────────────────┐   WebRTC (DTLS-SRTP/Opus, UDP)   ┌───────────┐  SIP+RTP  ┌────────────┐
   │  your app + Tring    │◀───────── media ──────────────▶│  GATEWAY   │──────────▶│ FreeSWITCH │
   │  WebRTC peer         │   signaling: HTTPS or WSS        │  :8087     │           │  (private) │
   └─────────────────────┘                                  └───────────┘           └────────────┘
```

The client is a **WebRTC peer that talks only to the gateway**. It never speaks
SIP and never sees FreeSWITCH. The gateway terminates the client's encrypted
media and bridges it into a private FreeSWITCH as a SIP B2BUA. So a native SDK
needs exactly three things: a WebRTC peer connection, an HTTP client, and (for
agents) a WebSocket client.

### Two roles, two signaling channels

| Role | Who | Signaling | Endpoint |
|---|---|---|---|
| **Visitor** | a caller dialing *into* an IVR flow (support "call us") | one-shot **HTTPS** offer/answer | `POST {gateway}/webrtc/offer` |
| **Agent** | a user who *receives* calls (CRM/contact-centre) | persistent **WebSocket** | `{gateway-ws}/agent/ws` |

Pick the role your app needs; they're independent.

### Authentication (tokens, minted server-side)

The client never holds the tenant API key. Your backend mints a short-lived JWT
(HS256) from the Tring API and hands it to the client. Two token types, both
verified by the gateway with no DB lookup (everything rides in the claims):

| Token | Audience | Claims | Minted by | Presented to |
|---|---|---|---|---|
| **Agent JWT** | `tring.sdk` | `tenant`, `sip_user`, `sip_domain` | `GET /v1/agents/{id}/token` (tenant API key) | `/agent/ws` (required) |
| **Gateway call token** | `tring.gateway` | `tenant`, `flow_id`, optional `client_ref` | `POST /v1/guest-credentials` (tenant API key, backend) | `/webrtc/offer` (required¹) — single-use (jti) |

Issuer is always `tring`, alg `HS256`. ¹ Required by default
(`gateway_require_auth=true`). There is **no public mint** — every visitor token
is minted server-side by the integrator's backend (which gates anonymous guests
with a CAPTCHA). The token's `flow_id`/`client_ref` are server-set (the browser
can't spoof identity), and it's consumed on first use so it can't be replayed.
(Set `gateway_require_auth=false` to allow a raw, token-less anonymous path —
still flow-pinned/can't reach a trunk, but the offer endpoint is then open.)

### Visitor call flow (simplest to port)

```
client                                   gateway
  │  (optional) GET your backend creds ── {gateway_url, flow_id, token?, ice_servers?}
  │
  │ 1. new PeerConnection(ice_servers)         // ice_servers usually empty (see ICE below)
  │ 2. add local mic audio track (Opus)
  │ 3. create DataChannel labeled "tring"      // for the end-of-call "bye" (see Teardown)
  │ 4. createOffer → setLocalDescription
  │ 5. wait for ICE gathering = complete       // NON-TRICKLE: full SDP in one shot
  │
  │ 6. POST {gateway_url}/webrtc/offer
  │      Authorization: Bearer <token>         // omit for anonymous
  │      { "sdp": "<offer SDP>", "flow_id": "<uuid>" }
  │ ───────────────────────────────────────▶
  │ ◀─────────────────────────────────────── 200 { "sdp": "<answer SDP>" }
  │ 7. setRemoteDescription(answer)            // media flows; you're in the IVR
  │
  │  DTMF  → RFC 4733 telephone-event on the audio track (insertDTMF)
  │  end   → see Teardown
```

Errors: `400 {"error":"sdp required"}`, `401 {"error":"invalid or expired call token"}` /
`{"error":"call token required"}`, `500 {"error":"negotiation failed"}`.

### Agent call flow (WebSocket signaling)

Open `wss://{gateway-host}/agent/ws?token={agentJWT}` (derive `ws/wss` from the
gateway's `http/https`; the token goes in the **query string** because a WS
handshake can't set `Authorization` from a browser — native clients may use the
`Authorization: Bearer` header instead). Then exchange JSON envelopes:

```jsonc
{ "type": "...", "call_id": "...", "sdp": "...", "from": "...", "auto": false, "reason": "..." }
```

| `type` | Direction | Fields | Meaning |
|---|---|---|---|
| `ready` | server→client | — | socket up, presence registered (you're "online") |
| `incoming` | server→client | `call_id`, `from`, `auto` | a call is ringing. `from` is a backend label, **never the dialed number** (may be empty → generic). `auto:true` ⇒ a backend click-to-call the agent already initiated → auto-answer instead of prompting Accept/Decline. |
| `offer` | client→server | `call_id`, `sdp` | accept the call with your WebRTC offer |
| `answer` | server→client | `call_id`, `sdp` | gateway's answer — apply it, call is connected |
| `reject` | client→server | `call_id` | decline a ringing call |
| `hangup` | client→server | `call_id` | end an active call |
| `ended` | server→client | `call_id`, `reason` | the call ended remotely — tear down |

The agent has **no API access**: it never originates. Outbound calls are placed
by the integrator's backend via `POST /v1/calls` (tenant API key, with the
number + optional `agent_label`); the agent's leg then arrives here as an
`incoming` with `auto:true`. So the agent's browser never receives the dialed
number — only the label.

Accept sequence: on `incoming` → build a PeerConnection, add mic, `createOffer`,
wait for ICE gathering, send `{type:"offer", call_id, sdp}` → receive
`{type:"answer", call_id, sdp}` → `setRemoteDescription`. The same WebRTC media
rules below apply. (`auto:true` ⇒ do this immediately; else prompt the user.)

**Keep-alive:** the gateway sets a ~90 s read deadline; send any message (a ping)
periodically so an idle socket isn't reaped. On socket close, treat every live
call as ended.

### WebRTC media requirements (both roles)

- **Codecs:** offer **Opus** (`opus/48000/2`) for audio and **`telephone-event/8000`**
  for DTMF (RFC 4733). The gateway negotiates these; DTMF as in-band tones is not
  supported.
- **Encryption:** standard WebRTC **DTLS-SRTP**. The browser/native leg is always
  encrypted; the gateway↔FS leg is plain RTP on the private network.
- **Transport muxing:** use **BUNDLE + rtcp-mux** (the default). Audio, RTCP, and
  the data channel then share one UDP port — no extra firewall ports.
- **Non-trickle ICE:** signaling is one-shot (there's no channel to trickle
  candidates on the visitor path), so **gather all ICE candidates before sending
  the SDP** (wait for `iceGatheringState == complete`, with a ~2 s safety
  timeout). Send the full local description.
- **ICE servers:** *none* by default. The gateway is a public host, so the client
  reaches it on its server-reflexive path without STUN (works through symmetric
  NAT). Provide **TURN** only for UDP-blocked networks — it arrives in
  `ice_servers` from the credentials mint, or you configure it yourself.
- **Header extensions:** don't rely on RTP header extensions — the gateway strips
  unknown ones on the path into FreeSWITCH.

### Teardown (end of call)

- **Data channel "bye"** (bidirectional, visitor path): the client opens a
  DataChannel labeled exactly `tring`. When either side ends the call it sends the
  text `bye` on it; the receiver ends immediately. This avoids waiting on slow
  ICE/DTLS disconnect timers. **Always also** treat PeerConnection state
  `failed`/`closed`/`disconnected` as "ended" (the fallback if `bye` is missed).
- **Agent path:** the gateway sends `{type:"ended"}` over the WebSocket; the
  client sends `{type:"hangup"}` to end its side.

### Minimal port checklist (e.g. Flutter / iOS)

1. HTTP client + (for agents) WebSocket client.
2. A WebRTC peer (mic capture, Opus, DTLS-SRTP, BUNDLE, `RTCDataChannel`,
   `insertDtmf`/`OnDtmf` on the audio sender).
3. Visitor: implement the 7-step offer/answer above. Agent: implement the WS
   envelope state machine above.
4. Gather ICE fully before sending SDP (non-trickle).
5. Open the `tring` data channel and handle `bye` both ways.
6. Get tokens from *your* backend, never embed the tenant API key.

Reference implementations to read alongside this: `src/gateway.ts` (visitor),
`src/agent-gateway.ts` (agent), `src/guest.ts` / `src/tring.ts` (orchestration).
The server side is `internal/gateway/{server,agent,fsuas,fsleg}.go`.

## License

Apache-2.0.
