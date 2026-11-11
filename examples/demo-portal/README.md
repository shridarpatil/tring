# Tring web-calling demos (one Flask backend)

A tiny Flask app that hosts **all three web-calling flows** so you can test them
without running separate servers. Open <http://localhost:5000> for a landing page
that links to each:

| Path | Flow | Widget | Auth |
|---|---|---|---|
| `/support` | **Anonymous "call us"** — visitor with no account lands in your IVR | `voice.js` (`data-credentials-url` → `/support-credentials`), **inline button** (`data-render-button="false"` + your own `<button data-tring-call>`) | guest token minted server-side, no `client_ref` (gate with CAPTCHA in prod); flow-pinned, can't reach a trunk |
| `/portal` | **Logged-in support** — call attributed to the verified client | `voice.js` (`data-credentials-url`), **floating launcher** | backend mints a trusted call token (`client_ref`) |
| `/crm` | **Agent** — receives/handles calls; the *backend* places outbound (agent never sees the number) | `agent.js` (`data-token-url`) — can't originate; its agent JWT only reaches the gateway WS and the self call-control routes (`/v1/sdk/*`) | backend mints a short-lived agent JWT; requires the demo login |

The browser never holds the tenant API key, and the **agent's browser never sees
the dialed number**. The SDK can't place calls: its agent JWT is accepted only by
the gateway WebSocket and the agent's own call-control routes (`POST
/v1/sdk/calls/hold|resume|transfer`, `GET /v1/sdk/transfer-targets`). This
backend mints tokens and places calls server-side:

```
browser ──/guest-credentials──► this app ──► Tring POST /v1/guest-credentials  (call token + client_ref)
browser ──/agent/token────────► this app ──► Tring GET  /v1/agents/<id>/token  (agent JWT, for the WS)
agent clicks a customer row (sends an id, NOT a number)
       ──/agent/originate──────► this app ──► Tring POST /v1/calls { agent_id, to:<number>, agent_label }
                  ◄── token ───┘
browser → Tring widget → raw WebRTC to the media gateway → call placed (FreeSWITCH stays private)
```

See `docs/src/content/docs/architecture/overview.md` (trusted-identity sequence) and the SDK README's
"How it works — protocol reference" for the full wire details.

## Prerequisites

- A running Tring stack (e.g. `make up`) reachable at `TRING_API_BASE`.
- The **media gateway** reachable by the browser at `TRING_GATEWAY_URL`
  (raw WebRTC — FreeSWITCH stays private).
- A **tenant API key** (`tk_live_…`) for all three flows, including anonymous
  `/support` calls. Server-side only — never put it in a page.
- A flow for visitors to land in: the tenant's **support flow** (console →
  Settings) or `TRING_FLOW_ID`.
- An **agent** for `/crm`: the tenant's first agent is used automatically, or set
  `TRING_AGENT_ID`. The agent's browser session at `/crm` must stay open to
  receive transferred calls.

> All visitor tokens are minted **server-side** via `POST /v1/guest-credentials`
> (tenant API key) — there is no public mint. `/support` mints a guest token (no
> `client_ref`); `/portal` mints with the verified `client_ref`. In production,
> gate the guest mint (`/support-credentials` here) with a CAPTCHA.

## Run

```bash
cd examples/demo-portal
pip install -r requirements.txt

export TRING_API_BASE=http://localhost:8085      # Tring REST API
export TRING_GATEWAY_URL=http://localhost:8087   # media gateway (browser-reachable)
export TRING_API_KEY=tk_live_xxxxxxxxxxxx         # tenant API key (all flows)
export TRING_FLOW_ID='your-flow-uuid'             # IVR flow visitors land in
export TRING_AGENT_ID='your-agent-uuid'           # optional; else the first agent

python app.py
```

The app listens on `127.0.0.1:5000` with the Flask debugger off. To reach it
from another machine or turn the debugger on, opt in explicitly — and only on a
trusted network (the debugger runs arbitrary code, and these routes spend your
tenant API key):

| Variable | Default | Effect |
|---|---|---|
| `DEMO_HOST` | `127.0.0.1` | Bind address, e.g. `0.0.0.0` |
| `DEMO_PORT` | `5000` | Port |
| `FLASK_DEBUG` | off | `1` enables the Werkzeug debugger and reloader |
| `FLASK_SECRET` | random per start | Session-signing key |

Open <http://localhost:5000> and pick a demo:
- **/support** — click **Call support** (no login).
- **/portal** — log in as **`alice` / `demo`** (or `bob` / `demo`), click **Call
  support**. In the console → **Calls**, the row's **From** shows `cust_ACME_42`
  (alice's `client_ref`), not `guest_…`.
- **/crm** — sign in (any demo user), then click a customer's **Call**, or keep
  it open to catch an IVR transfer. `/crm` and its `/agent/*` endpoints mint
  agent tokens and place calls with your API key, so they require the login.

End-to-end test: open `/crm` in one tab (agent online) and `/support` in another
(visitor), call support, and have the IVR transfer to the agent.

## Set up calling on your portal

1. In Tring, create an agent and an active flow that transfers to that agent or
   their team. Select it as the default support flow in **Settings**, or pass
   its UUID when minting visitor credentials.
2. Add a visitor credentials endpoint to your portal backend. Check the user's
   session, then call `POST /v1/guest-credentials` with the tenant API key and
   `{ "flow_id": "your-flow-uuid", "client_ref": "verified-customer-id" }`.
   Return Tring's JSON unchanged with `Cache-Control: no-store`. For anonymous
   callers, omit `client_ref` and apply your CAPTCHA/rate limit checks.
3. Add the visitor widget to the customer page:

   ```html
   <button data-tring-call>Call support</button>
   <script src="https://tring.example.com/sdk/voice.js"
           data-credentials-url="/tring/visitor-credentials"
           data-render-button="false"
           data-title="Support"></script>
   ```

4. Add an agent token endpoint to the agent portal backend. Check the agent's
   session, call `GET /v1/agents/{id}/token` for that agent using the tenant API
   key, and return the JSON. Add the agent widget to that page:

   ```html
   <script src="https://tring.example.com/sdk/agent.js"
           data-token-url="/tring/agent-token"></script>
   ```

5. Upload waiting music to Tring's audio library and select it under
   **Settings → Hold music**. A team's hold music overrides the tenant setting.
   Music plays while routing rings or queues the call and stops when the
   answering agent's media connects. Without a configured file, waiting is
   silent. IVR prompts play separately.
6. Enable **Record calls** if recordings are required. Both participants can
   enable **Camera** during the call. Video recordings show agent left and
   client right, include both voices, and show labelled placeholders while a
   camera is off. Review recordings in the Tring console's **Calls** page.

Replace `https://tring.example.com` with your Tring API URL. Serve production
portal pages over HTTPS and make the gateway reachable by browsers; configure
TURN where direct UDP is blocked. An iframe must allow camera, microphone, and
fullscreen access. Keep both credentials endpoints on their portal's origin.

The client panel opens compactly, with **Keypad**, **Camera**, **Share**, and
an explicit video **Full screen** control. It shows **Calling…** until the
agent accepts, then starts the timer at **00:00**. Custom clients should start
timers on `TringGuest`'s `answered` event; `connected` means gateway/IVR media
is ready and can happen before an agent answers.

Detailed guides:

- [Visitor widget and backend examples](../../docs/src/content/docs/sdk/visitor-widget.md)
- [Agent widget and token endpoint](../../docs/src/content/docs/sdk/agent-widget.md)
- [JavaScript API and events](../../docs/src/content/docs/sdk/javascript-api.md)
- [Video recordings](../../docs/src/content/docs/features/recording-and-call-logs.md)

## How it maps to your real app

- `DEMO_USERS` → your real auth / customer store. `client_ref` is whatever you
  want attributed on the call log (a CRM id, account number, …).
- `/login` → your existing login.
- `/guest-credentials` → the one endpoint you add for trusted visitor calls:
  check the session, call Tring's authenticated mint with the verified
  `client_ref`, return the JSON. `voice.js` calls it via `data-credentials-url`.
- `/agent/token` → mint a fresh agent JWT for the logged-in agent. `agent.js`
  calls it via `data-token-url`. Gate it (and `/agent/originate`,
  `/agent/availability`) on your agent auth — the demo just requires a session.
