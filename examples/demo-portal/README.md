# Tring web-calling demos (one Flask backend)

A tiny Flask app that hosts **all three web-calling flows** so you can test them
without running separate servers. Open <http://localhost:5000> for a landing page
that links to each:

| Path | Flow | Widget | Auth |
|---|---|---|---|
| `/support` | **Anonymous "call us"** — visitor with no account lands in your IVR | `voice.js` (`data-credentials-url` → `/support-credentials`), **inline button** (`data-render-button="false"` + your own `<button data-tring-call>`) | guest token minted server-side, no `client_ref` (gate with CAPTCHA in prod); flow-pinned, can't reach a trunk |
| `/portal` | **Logged-in support** — call attributed to the verified client | `voice.js` (`data-credentials-url`), **floating launcher** | backend mints a trusted call token (`client_ref`) |
| `/crm` | **Agent** — receives/handles calls; the *backend* places outbound (agent never sees the number) | `agent.js` (`data-token-url`) — WS-only, no API access | backend mints a short-lived agent JWT |

The browser never holds the tenant API key, and the **agent's browser never sees
the dialed number**. The SDK has no Tring API access; this backend mints tokens
and places calls server-side:

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
- A **tenant API key** (`tk_live_…`) for the trusted flows. Server-side only —
  never put it in a page. (The anonymous `/support` flow works without it.)
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
pip install -r requirements.txt

export TRING_API_BASE=http://localhost:8085      # Tring REST API
export TRING_GATEWAY_URL=http://localhost:8087   # media gateway (browser-reachable)
export TRING_API_KEY=tk_live_xxxxxxxxxxxx         # tenant API key (trusted flows)
export TRING_FLOW_ID=<flow-uuid>                  # IVR flow visitors land in
export TRING_AGENT_ID=<agent-uuid>                # optional; else the first agent

python app.py
```

Open <http://localhost:5000> and pick a demo:
- **/support** — click **Call support** (no login).
- **/portal** — log in as **`alice` / `demo`** (or `bob` / `demo`), click **Call
  support**. In the console → **Calls**, the row's **From** shows `cust_ACME_42`
  (alice's `client_ref`), not `guest_…`.
- **/crm** — click a customer's **Call**, or keep it open to catch an IVR transfer.

End-to-end test: open `/crm` in one tab (agent online) and `/support` in another
(visitor), call support, and have the IVR transfer to the agent.

## How it maps to your real app

- `DEMO_USERS` → your real auth / customer store. `client_ref` is whatever you
  want attributed on the call log (a CRM id, account number, …).
- `/login` → your existing login.
- `/guest-credentials` → the one endpoint you add for trusted visitor calls:
  check the session, call Tring's authenticated mint with the verified
  `client_ref`, return the JSON. `voice.js` calls it via `data-credentials-url`.
- `/agent/token` → mint a fresh agent JWT for the logged-in agent. `agent.js`
  calls it via `data-token-url`.
