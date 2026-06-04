# @tring/voice

Drop-in browser voice SDK for the Tring CPaaS — WebRTC calling on any web page, no SIP/FreeSWITCH knowledge needed. Built into a few bundles, all of which the **Tring API serves itself at `/sdk/*`** (so self-hosters need no CDN):

| Bundle | Served at | For |
|---|---|---|
| **`voice.js`** (`dist/voice.iife.js`) | `/sdk/voice.js` | **Website visitors** — a "Call" button on any public page drops the caller into your IVR over WebRTC, no account. Anonymous (`data-gateway-url`+`data-flow-id`) or trusted (`data-credentials-url`). |
| **`agent.js`** (`dist/agent.iife.js`) | `/sdk/agent.js` | **CRM agents** — auto-binds `<button data-tring-agent-call>` and renders a floating call panel to place/receive calls. Auth: short-lived agent **JWT**. |
| **`tring.js`** (`dist/tring.iife.js`) | `/sdk/tring.js` | Bare SDK — exposes `window.Tring` for programmatic use. |
| **`tring.esm.js`** | — (npm) | `import Tring from '@tring/voice'` for bundlers. |

> The widget is served from the same origin as your API, e.g. `https://your-tring-host/sdk/voice.js`. No `cdn.tring.io` dependency — point `src` at your own deployment.

> **Building for another platform (Flutter / iOS / Android)?** This is a thin
> client over a small, language-agnostic WebRTC protocol — see
> [**How it works — protocol reference**](#how-it-works--protocol-reference-port-the-sdk-to-any-platform)
> below. It's the full wire spec; the TypeScript is just the reference implementation.

## Quickstart — drop-in widget (the laziest possible integration)

Add **one script tag** to your CRM template. Buttons with `data-tring-agent-call data-to="..."` become callable. A floating panel handles the in-call UI.

```html
<table>
  <tr>
    <td>Aanya Sharma</td>
    <td>+919876543210</td>
    <td><button data-tring-agent-call data-to="+919876543210">Call</button></td>
  </tr>
</table>

<script src="https://your-tring-host/sdk/agent.js"
        data-api-base="https://your-tring-host"
        data-gateway-url="https://your-gateway-host"
        data-token-url="/tring/token"></script>
```

The agent connects to the media gateway over raw WebRTC (`data-gateway-url` is required — no SIP in the browser). `/tring/token` is **your** CRM endpoint that mints a fresh JWT server-side (using your tenant API key) for the currently-logged-in agent. The browser never sees the API key. For static-token mode (e.g. server-rendered pages), use `data-token="<jwt>"` instead of `data-token-url`.

You can also dial from a free-form input by pointing at it:

```html
<input id="number" type="text">
<button data-tring-agent-call data-to-target="#number">Call</button>
```

## Website visitor calling (`voice.js`)

Put voice on a public page so visitors call **into your IVR** — no login, no phone number. Anonymous "call us" is one tag pointing at your gateway and the flow to land in:

```html
<script src="https://your-tring-host/sdk/voice.js"
        data-gateway-url="https://your-gateway-host"
        data-flow-id="<flow-uuid>"></script>
```

The visitor taps the floating "Call" button (or your own `<button data-tring-call>`), gets an anonymous, flow-pinned identity, and lands in your IVR — the call is pinned to that flow server-side and can never reach a trunk. To attribute a **logged-in** caller instead, point `data-credentials-url` at your backend, which mints a gateway call token carrying the verified `client_ref` (see `examples/byoc-portal/`). See also `examples/support-portal.html`.

## Programmatic use

The browser needs a **short-lived JWT**, not the tenant API key. Your CRM's backend mints the JWT using `GET /v1/agents/<id>/token` (auth: tenant API key) and ships it to the browser.

```html
<script src="https://your-tring-host/sdk/tring.js"></script>
<script>
  const tring = await Tring.init({
    token: 'eyJ...',                     // JWT from /v1/agents/<id>/token (5-min TTL)
    apiBase: 'https://api.tring.io',
    gatewayUrl: 'https://gw.tring.io',   // required — raw WebRTC to the media gateway
  });

  tring.on('registered',    ()       => console.log('ready'));   // WS to gateway up
  tring.on('call:ringing',  ({call}) => console.log('ringing', call.to));
  tring.on('call:answered', ()       => console.log('connected'));
  tring.on('call:ended',    ()       => console.log('ended'));

  const call = await tring.call('+919876543210');
  // ...later:
  await call.hangup();
</script>
```

Inbound calls (and the agent's leg of an originated call) are auto-accepted by default. Set `autoAnswer: false` in `init` to gate on user UI.

## Development

```bash
npm install
npm run build      # produces dist/tring.{iife,esm}.js + .map + .d.ts
npm run watch      # rebuild on change
npm run typecheck  # tsc --noEmit
```

## Smoke test in a real browser

1. Have the Tring API + orchestrator + gateway + FreeSWITCH running locally (root `docker compose up -d`, or `go run ./cmd/api` + `go run ./cmd/orchestrator` + `go run ./cmd/gateway`).
2. Serve the `sdk/js/` tree over HTTP — `file://` URLs trip CORS for `fetch`:
   ```bash
   python3 -m http.server 9999 --directory ~/workspace/tring/sdk/js
   ```
3. Mint a JWT:
   ```bash
   KEY='tk_test_dev_local_only_change_me_in_prod'
   AGENT=$(curl -sS -H "Authorization: Bearer $KEY" http://localhost:8085/v1/agents | jq -r '.[0].id')
   curl -sS -H "Authorization: Bearer $KEY" http://localhost:8085/v1/agents/$AGENT/token | jq -r .token
   ```
4. Open one of:
   - `http://localhost:9999/examples/demo-crm.html` — the **drop-in widget** demo (paste JWT into the form, click Load widget, then click any Call button).
   - `http://localhost:9999/examples/test.html` — the bare-SDK manual test (Connect / Call / Hang up).

## Note: no browser WSS / cert dance

Since the ADR-001 cutover the browser peers with the **media gateway** over raw WebRTC (HTTP signaling on `:8087`) — there's no SIP-over-WSS to FreeSWITCH and no self-signed `:7443` cert to pre-accept. For local dev on one machine, point everything at loopback (`TRING_INTERNAL_IP=127.0.0.1`); for production, put TLS in front of the gateway (`https://` + WSS on `:8087`) and set `TRING_GATEWAY_PUBLIC_URL`. See `docs/EDGE_DEPLOYMENT.md`.

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
| **Gateway call token** | `tring.gateway` | `tenant`, `flow_id`, optional `client_ref` | `POST /v1/guest-credentials` (API key, trusted) or `POST /v1/sdk/guest-credentials` (public) | `/webrtc/offer` (optional¹) |

Issuer is always `tring`, alg `HS256`. ¹ The visitor token is optional unless
the gateway runs with `gateway_require_auth=true`; anonymous visitors instead
pass `flow_id` in the offer body. A trusted-mint token's `flow_id`/`client_ref`
**override** anything in the body (the browser can't spoof identity).

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
{ "type": "...", "call_id": "...", "sdp": "...", "from": "...", "reason": "..." }
```

| `type` | Direction | Fields | Meaning |
|---|---|---|---|
| `ready` | server→client | — | socket up, presence registered (you're "online") |
| `incoming` | server→client | `call_id`, `from` | a call is ringing for you |
| `offer` | client→server | `call_id`, `sdp` | accept the call with your WebRTC offer |
| `answer` | server→client | `call_id`, `sdp` | gateway's answer — apply it, call is connected |
| `reject` | client→server | `call_id` | decline a ringing call |
| `hangup` | client→server | `call_id` | end an active call |
| `ended` | server→client | `call_id`, `reason` | the call ended remotely — tear down |

Accept sequence: on `incoming` → build a PeerConnection, add mic, `createOffer`,
wait for ICE gathering, send `{type:"offer", call_id, sdp}` → receive
`{type:"answer", call_id, sdp}` → `setRemoteDescription`. The same WebRTC media
rules below apply.

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
