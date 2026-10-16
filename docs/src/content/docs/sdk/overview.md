---
title: Browser SDK overview
description: What the Tring browser SDK is, which bundles it ships, how the API serves them, and how credentials stay on your server.
---

The Tring browser SDK puts WebRTC calling on any web page. It comes in two forms: drop-in widgets that you add with one `<script>` tag, and a small JavaScript API for building your own UI. The browser talks only to the Tring media gateway. It never speaks SIP and never reaches FreeSWITCH.

The source lives in [`sdk/js/`](https://github.com/shridarpatil/tring/tree/main/sdk/js). There is a runnable demo in [`examples/demo-portal/`](https://github.com/shridarpatil/tring/tree/main/examples/demo-portal).

## Bundles

| Bundle | Built from | Served at | Use it for |
|---|---|---|---|
| `voice.js` | `dist/voice.iife.js` | `/sdk/voice.js` | **Website visitors.** A "Call" button that drops the visitor into one of your IVR flows. No account needed. See [Visitor widget](/tring/sdk/visitor-widget/). |
| `agent.js` | `dist/agent.iife.js` | `/sdk/agent.js` | **Agents in your CRM.** Keeps the agent online and shows a call panel when a call rings them. It **cannot place calls**. See [Agent widget](/tring/sdk/agent-widget/). |
| `tring.js` | `dist/tring.iife.js` | `/sdk/tring.js` | **The bare SDK.** Sets `window.Tring` to the agent client class and renders no UI. See [JavaScript API](/tring/sdk/javascript-api/). |
| `tring.esm.js` | `dist/tring.esm.js` | not served | **Bundlers.** An ES module with every public export (`Tring`, `TringGuest`, `RtcCall`, `RtcAgent`, and the protocol constants). |

Each script bundle also sets one global:

- `voice.js` sets `window.TringGuest`
- `agent.js` and `tring.js` set `window.Tring`

## How the bundles are served

The Tring API serves the bundles from its own origin, under `/sdk/*`, so you don't need a CDN:

```html
<script src="https://tring.example.com/sdk/voice.js" …></script>
```

- `make sdk` builds the bundles and copies `voice.iife.js`, `agent.iife.js` and `tring.iife.js` into the API binary as `voice.js`, `agent.js` and `tring.js`. The Docker image does the same in its Node build stage. If the binary was built without the bundles, `/sdk/*` isn't mounted and these URLs return 404.
- The URLs don't carry a version, so the API sends `Cache-Control: no-cache`. Browsers revalidate each load, get a cheap `304` when nothing changed, and pick up a new bundle as soon as you deploy it.
- `tring.esm.js` isn't served. Build it from `sdk/js` (`npm install && npm run build`) and import it in your app. The package is named `@tring/voice`.

## Trust model

The browser never holds your tenant API key. Your backend keeps the key and mints short-lived credentials for each browser:

```
browser ──(your session)──► your backend ──(tenant API key)──► Tring API
   ▲                              │
   └──── short-lived token ◄──────┘
browser ──(token)──► Tring media gateway  /rtc  (WebSocket signaling + WebRTC media)
```

| Who | Your backend calls | Token | Lifetime |
|---|---|---|---|
| Visitor (`voice.js`) | `POST /v1/guest-credentials` | Gateway call token. The flow is pinned inside the token, and an optional `client_ref` goes on the call record. | 2 minutes, **single use** |
| Agent (`agent.js`, `tring.js`) | `GET /v1/agents/{id}/token` | Agent JWT, scoped to one agent | 5 minutes. The widgets fetch a new one on every reconnect. |

What this gives you:

- **The flow is set by your server.** A visitor token carries its `flow_id`, and the gateway uses that flow, whatever the page sends. A visitor call can only run an IVR flow and can never dial a phone trunk.
- **Agents can't originate.** The agent JWT authenticates the gateway socket and a few self-service call-control endpoints (`/v1/sdk/*`). It cannot place calls. Outbound calls go through your backend with `POST /v1/calls` (see [Calls API](/tring/api-reference/calls/)), so the agent's browser never sees the number it is calling.
- **Tokens can't be replayed.** The gateway accepts each visitor token once, on its first join.

:::danger
Keep your tenant API key (`tk_live_…`) on the server, in an environment variable or a secret manager. Never put it in page source, a JavaScript bundle or a URL. If one leaks, revoke it in the console and issue a new one.
:::

Put the credential endpoints you build behind your own authentication. For anonymous visitors, gate the endpoint with a CAPTCHA and a rate limit. See [API keys](/tring/api-reference/api-keys/) and [Authentication](/tring/api-reference/authentication/).

## Browser requirements

- **A secure context.** The microphone needs HTTPS (`localhost` also counts). On plain `http://`, mobile Safari has no `navigator.mediaDevices` at all, and the SDK fails with "Microphone needs HTTPS".
- **WebRTC, `getUserMedia` and WebSocket.** The bundles target Chrome 100+, Edge 100+, Firefox 100+ and Safari 15+.
- **A user gesture for the microphone on iOS.** The visitor widget asks for the mic as soon as the visitor taps Call, before any network request, because iOS Safari only grants it while the tap is still active. Screen sharing (`getDisplayMedia`) also needs a click.
- **Network access to the gateway.** The browser must reach `TRING_GATEWAY_PUBLIC_URL` over HTTPS/WSS (signaling) and UDP (media). Users on networks that block UDP need TURN, configured on the server; TURN servers reach the browser in the `ice_servers` field of the credential responses.

## Production checklist

- Serve your pages, the Tring API and the gateway over **HTTPS**. The SDK changes `https://` to `wss://` for signaling.
- Set `TRING_GATEWAY_PUBLIC_URL` to a URL the browser can reach. Both credential endpoints return it as `gateway_url`.
- The API currently allows CORS from any origin (it echoes the `Origin` header). That's safe for the SDK's own calls, which authenticate with a bearer token, but don't rely on CORS to protect anything.
- **Content Security Policy.** The SDK doesn't set a CSP. If your page has one, allow:
  - `script-src`: your Tring API origin
  - `connect-src`: the gateway's `wss://` origin, plus the Tring API origin if agents use transfer (`agent.js` calls `/v1/sdk/transfer-targets`)
  - `style-src 'unsafe-inline'`: both widgets add a `<style>` element at runtime
- **Permissions Policy.** If you embed the widget in an iframe, the iframe needs `allow="microphone"`. Add `display-capture` too if you want screen sharing.

## Next

- [Visitor widget (voice.js)](/tring/sdk/visitor-widget/)
- [Agent widget (agent.js)](/tring/sdk/agent-widget/)
- [JavaScript API](/tring/sdk/javascript-api/)
- [Web calling](/tring/features/web-calling/) and [Call control](/tring/features/call-control/) explain the features behind the widgets.
