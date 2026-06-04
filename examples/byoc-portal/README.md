# BYOC portal — bring-your-own-credentials calling demo

A tiny Flask app standing in for a **logged-in support portal**. It shows the
trusted web-calling flow: the portal backend mints Tring guest credentials
server-side (with the tenant API key and the *verified* logged-in client id),
so the browser never holds the API key and never calls the public mint. The
call then shows up in Tring's call log as the **client**, not `guest_…`.

See `docs/WEB_CALL_FLOW.md` for the full sequence.

```
browser (logged in) ──GET /guest-credentials──► this Flask app
                                                 └─► Tring POST /v1/guest-credentials
                                                     (Bearer <API key> + client_ref)
                     ◄── { gateway_url, flow_id, token, ice_servers } ──┘
                         (token is a JWT carrying the verified client_ref)
browser → voice widget → raw WebRTC to the media gateway → call shows as client_ref
```

## Prerequisites

- A running Tring stack (e.g. `make up`) reachable at `TRING_API_BASE`.
- A **tenant API key** (`tk_live_…`). Server-side only — never put it in a page.
- A flow for guests to land in: either set it as the tenant's **support flow**
  (console → Settings) or pass its id via `TRING_FLOW_ID`.
- The browser must be able to reach the Tring API for the widget bundle
  (`/sdk/voice.js`) and the **media gateway** `gateway_url` returned in the creds
  (raw WebRTC — FreeSWITCH stays private). For local dev with `make up`, set
  `TRING_GATEWAY_PUBLIC_URL` to a browser-reachable gateway URL.

> Optional: in the console (Settings → guest) enable **"Require backend-minted
> credentials"** to disable the public mint entirely — then this trusted flow
> is the *only* way to place a web call for your tenant.

## Run

```bash
pip install -r requirements.txt

export TRING_API_BASE=http://localhost:8085      # your Tring API
export TRING_API_KEY=tk_live_xxxxxxxxxxxx          # your tenant API key
export TRING_FLOW_ID=<flow-uuid>                   # optional; else tenant support flow

python app.py
```

Open <http://localhost:5000>, log in as **`alice` / `demo`** (or `bob` / `demo`),
and click **Call support**. In the Tring console → **Calls**, the new row's
**From** shows `cust_ACME_42` (alice's `client_ref`) instead of `guest_…`.

## How it maps to your real app

- `DEMO_USERS` → your real auth / customer store. `client_ref` is whatever you
  want attributed on the call log (a CRM id, account number, …).
- `/login` → your existing login.
- `/guest-credentials` → the one endpoint you add: check the session, call
  Tring's authenticated mint with the verified `client_ref`, return the JSON.
  The widget calls it via `data-credentials-url` with the session cookie.
