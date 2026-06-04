# @tring/voice

Drop-in browser voice SDK for the Tring CPaaS — WebRTC calling on any web page, no SIP/FreeSWITCH knowledge needed. Built into a few bundles, all of which the **Tring API serves itself at `/sdk/*`** (so self-hosters need no CDN):

| Bundle | Served at | For |
|---|---|---|
| **`voice.js`** (`dist/voice.iife.js`) | `/sdk/voice.js` | **Website visitors** — a "Call" button on any public page drops the caller into your IVR over WebRTC, no account. Anonymous (`data-gateway-url`+`data-flow-id`) or trusted (`data-credentials-url`). |
| **`agent.js`** (`dist/agent.iife.js`) | `/sdk/agent.js` | **CRM agents** — auto-binds `<button data-tring-agent-call>` and renders a floating call panel to place/receive calls. Auth: short-lived agent **JWT**. |
| **`tring.js`** (`dist/tring.iife.js`) | `/sdk/tring.js` | Bare SDK — exposes `window.Tring` for programmatic use. |
| **`tring.esm.js`** | — (npm) | `import Tring from '@tring/voice'` for bundlers. |

> The widget is served from the same origin as your API, e.g. `https://your-tring-host/sdk/voice.js`. No `cdn.tring.io` dependency — point `src` at your own deployment.

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

## License

Apache-2.0.
