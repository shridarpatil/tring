# @tring/voice

Drop-in browser voice SDK for the Tring CPaaS — WebRTC calling on any web page, no SIP/FreeSWITCH knowledge needed. Built into a few bundles, all of which the **Tring API serves itself at `/sdk/*`** (so self-hosters need no CDN):

| Bundle | Served at | For |
|---|---|---|
| **`voice.js`** (`dist/voice.iife.js`) | `/sdk/voice.js` | **Website visitors** — a "Call" button on any public page drops the caller into your IVR over WebRTC, no account. Auth: tenant **public key** (`pk_live_…`). |
| **`agent.js`** (`dist/agent.iife.js`) | `/sdk/agent.js` | **CRM agents** — auto-binds `<button data-tring-call>` and renders a floating call panel to place/receive calls. Auth: short-lived agent **JWT**. |
| **`tring.js`** (`dist/tring.iife.js`) | `/sdk/tring.js` | Bare SDK — exposes `window.Tring` for programmatic use. |
| **`tring.esm.js`** | — (npm) | `import Tring from '@tring/voice'` for bundlers. |

> The widget is served from the same origin as your API, e.g. `https://your-tring-host/sdk/voice.js`. No `cdn.tring.io` dependency — point `src` at your own deployment.

## Quickstart — drop-in widget (the laziest possible integration)

Add **one script tag** to your CRM template. Buttons with `data-tring-call data-to="..."` become callable. A floating panel handles the in-call UI.

```html
<table>
  <tr>
    <td>Aanya Sharma</td>
    <td>+919876543210</td>
    <td><button data-tring-call data-to="+919876543210">Call</button></td>
  </tr>
</table>

<script src="https://your-tring-host/sdk/agent.js"
        data-api-base="https://your-tring-host"
        data-token-url="/tring/token"></script>
```

`/tring/token` is **your** CRM endpoint that mints a fresh JWT server-side (using your tenant API key) for the currently-logged-in agent. The browser never sees the API key. For static-token mode (e.g. server-rendered pages), use `data-token="<jwt>"` instead of `data-token-url`.

You can also dial from a free-form input by pointing at it:

```html
<input id="number" type="text">
<button data-tring-call data-to-target="#number">Call</button>
```

## Website visitor calling (`voice.js`)

Put voice on a public page so visitors call **into your IVR** — no login, no phone number. One tag:

```html
<script src="https://your-tring-host/sdk/voice.js"
        data-api-base="https://your-tring-host"
        data-public-key="pk_live_…"></script>
```

The `pk_live_…` **public key** is safe to embed (unlike the agent JWT/API key). It only works from the domains you allowlist in the console (**Settings → Guest web calling → Allowed domains**), which is what stops a stolen key from being used elsewhere. The visitor taps the floating "Call" button (or your own `<button data-tring-guest-call>`), gets an anonymous, flow-pinned identity, and lands in your IVR. See `examples/support-portal.html`.

## Programmatic use

The browser needs a **short-lived JWT**, not the tenant API key. Your CRM's backend mints the JWT using `GET /v1/agents/<id>/token` (auth: tenant API key) and ships it to the browser.

```html
<script src="https://your-tring-host/sdk/tring.js"></script>
<script>
  const tring = await Tring.init({
    token: 'eyJ...',                  // JWT from /v1/agents/<id>/token (5-min TTL)
    apiBase: 'https://api.tring.io',
  });

  tring.on('registered',    ()       => console.log('ready'));
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

1. Have the Tring API + orchestrator + FreeSWITCH running locally (root `docker compose up -d` + `go run ./cmd/api` + `go run ./cmd/orchestrator`).
2. Visit `https://localhost:7443` once and accept the self-signed cert (see cert gotcha below).
3. Serve the `sdk/js/` tree over HTTP — `file://` URLs trip CORS for `fetch`:
   ```bash
   python3 -m http.server 9999 --directory ~/workspace/tring/sdk/js
   ```
4. Mint a JWT:
   ```bash
   KEY='tk_test_dev_local_only_change_me_in_prod'
   AGENT=$(curl -sS -H "Authorization: Bearer $KEY" http://localhost:8085/v1/agents | jq -r '.[0].id')
   curl -sS -H "Authorization: Bearer $KEY" http://localhost:8085/v1/agents/$AGENT/token | jq -r .token
   ```
5. Open one of:
   - `http://localhost:9999/examples/demo-crm.html` — the **drop-in widget** demo (paste JWT into the form, click Load widget, then click any Call button).
   - `http://localhost:9999/examples/test.html` — the bare-SDK manual test (Connect / Call / Hang up).

## Known gotcha: self-signed WSS in dev

FreeSWITCH's `internal` profile exposes WSS on `wss://<host>:7443` with a **self-signed certificate** by default. Browsers refuse such connections silently in WebRTC contexts.

Workarounds for local dev:

- **Visit the cert once**: open `https://localhost:7443` in the same browser, accept the security warning. After that, the WSS connection inside the SDK succeeds.
- **Chrome flag**: launch Chrome with `--ignore-certificate-errors --user-data-dir=/tmp/tring-chrome` for a throwaway profile.
- **Production**: terminate WSS at a proper TLS-terminating proxy (nginx with Let's Encrypt) in front of FreeSWITCH.

This is a Phase 2 problem to solve cleanly; Phase 1 just documents it.

## License

Apache-2.0.
