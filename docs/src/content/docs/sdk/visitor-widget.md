---
title: Visitor widget (voice.js)
description: Add a "Call us" button to any page. Visitors call into your IVR over WebRTC, using credentials minted by your backend.
---

`voice.js` adds a "Call us" button to a web page. When a visitor taps it, the widget asks for the microphone, gets a call token from **your** backend, and connects the visitor to one of your IVR flows over WebRTC. The visitor needs no account and no phone number. The call can only run an IVR flow: it never reaches a phone trunk.

Out of the box, the widget renders:

- a floating launcher button in the bottom-right corner (you can use your own button instead)
- a full-screen call sheet with call status and a timer, a 12-key dialpad, and **Mute**, **Share** (screen) and **End** buttons
- a ringback tone while the call connects
- the agent's shared screen, if they share it

## How it works

```
visitor taps "Call"
  → widget asks for the microphone (inside the tap, for iOS)
  → GET  https://www.example.com/tring/visitor-credentials   (your backend, with the visitor's cookies)
        → POST https://tring.example.com/v1/guest-credentials (your backend → Tring, tenant API key)
        ← { gateway_url, flow_id, token, ice_servers }
  → widget opens wss://<gateway>/rtc?token=…  → visitor joins the IVR flow
```

- The widget fetches credentials with a **GET** request that includes cookies (`credentials: "include"`), so your session cookie identifies the visitor.
- It fetches **new credentials for every call**. A call token is valid for 2 minutes and works only once, so don't cache it.
- The flow is pinned inside the token. The page never names the flow, and anything the page sends is ignored for visitor tokens.

## Add it to a page

```html
<script src="https://tring.example.com/sdk/voice.js"
        data-credentials-url="/tring/visitor-credentials"
        data-button-label="Call support"
        data-title="Example Support"></script>
```

To use your own button instead of the floating launcher:

```html
<button data-tring-call>Talk to a human</button>

<script src="https://tring.example.com/sdk/voice.js"
        data-credentials-url="/tring/visitor-credentials"
        data-render-button="false"></script>
```

Clicking any element with the `data-tring-call` attribute starts a call. The widget watches the page for changes, so this also works for buttons your single-page app adds later. It binds `data-tring-call` elements even when the floating launcher is on.

## Attributes

These go on the `<script>` tag.

| Attribute | Default | Meaning |
|---|---|---|
| `data-credentials-url` | none | URL on **your** backend that returns `{ gateway_url, flow_id, token, ice_servers? }`. Fetched with GET, with cookies, once per call. This is the supported setup. |
| `data-button-label` | `Call support` | Text on the floating launcher. |
| `data-title` | `Support` | Heading on the call sheet. Its first letter is used as the avatar. |
| `data-render-button` | `true` | Set to `"false"` to hide the floating launcher. Calls then start only from `data-tring-call` elements. |
| `data-gateway-url` | none | Gateway base URL to connect to without a credentials endpoint. See the caution below. |
| `data-flow-id` | none | Flow UUID to use with `data-gateway-url`. |

The `<script>` tag needs `data-credentials-url` or `data-gateway-url`. Without either, the widget logs an error to the console and doesn't render. If both are set, the values returned by the credentials endpoint win.

:::caution
`data-gateway-url` and `data-flow-id` connect without a token. The current media gateway (`/rtc`) rejects any connection without a valid token (HTTP 401 on the WebSocket upgrade), so this setup fails and the call sheet shows "signaling socket error". There is no attribute for a static token. Use `data-credentials-url`.
:::

## Your credentials endpoint

The endpoint has one job: decide whether this visitor may call, then mint credentials with your API key and return the JSON to the browser.

**Request it makes to Tring:**

```http
POST /v1/guest-credentials
Authorization: Bearer <tenant API key>
Content-Type: application/json

{ "flow_id": "<optional flow UUID>", "client_ref": "<optional verified caller id>" }
```

| Field | Meaning |
|---|---|
| `flow_id` | The IVR flow to run. Leave it out to use the tenant's default flow (see [Tenant & guest config](/tring/api-reference/tenant/)). If neither is set, Tring returns `400`. |
| `client_ref` | Your identifier for a **logged-in** caller, for example a CRM customer id. It goes on the call record. Send it only after your backend has verified who the caller is. |

**Response (`200`):**

```json
{
  "gateway_url": "https://gw.tring.example.com",
  "flow_id": "11111111-2222-3333-4444-555555555555",
  "token": "eyJ…",
  "ice_servers": [{ "urls": ["stun:stun.example.com:3478"] }]
}
```

Return this to the browser unchanged. If you return an error, send JSON like `{"error": "…"}` with a non-2xx status. The widget shows the `error` text on the call sheet.

The two kinds of caller use the same endpoint and differ only in the checks:

| Caller | Check before minting | `client_ref` |
|---|---|---|
| Logged in | Verify the session | Send the caller's id |
| Anonymous | CAPTCHA and/or rate limit by IP or device | Leave it out |

:::danger
The tenant API key stays on your server. Load it from an environment variable or a secret manager. Never ship it to the browser. An unprotected credentials endpoint lets anyone place calls into your IVR and run up your costs, so always put your own auth, CAPTCHA or rate limit in front of it.
:::

### Node.js (Express)

:::note
This example uses Express. Check it against your organisation's approved-dependency list, pin its version, and review it for supply-chain risk before you install it. It uses the built-in `fetch` from Node 18+, so there are no other dependencies.
:::

```js
// server.js. Run with: TRING_API_URL=https://tring.example.com TRING_API_KEY=... node server.js
import express from "express";

const TRING_API_URL = process.env.TRING_API_URL;   // e.g. https://tring.example.com
const TRING_API_KEY = process.env.TRING_API_KEY;   // tk_live_…, from your secret manager
const TRING_FLOW_ID = process.env.TRING_FLOW_ID;   // optional; else the tenant default flow

const app = express();

// Replace with your real session lookup (cookie → user). Returns null if not logged in.
async function currentUser(req) {
  return null;
}

// Replace with your CAPTCHA check / rate limiter for anonymous visitors.
async function allowAnonymousCall(req) {
  return false;
}

app.get("/tring/visitor-credentials", async (req, res) => {
  const user = await currentUser(req);
  if (!user && !(await allowAnonymousCall(req))) {
    return res.status(401).json({ error: "Please sign in to call us." });
  }

  const body = {};
  if (TRING_FLOW_ID) body.flow_id = TRING_FLOW_ID;
  if (user) body.client_ref = user.customerId; // only a verified identity

  try {
    const r = await fetch(`${TRING_API_URL}/v1/guest-credentials`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${TRING_API_KEY}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify(body),
    });
    const payload = await r.json();
    res.set("Cache-Control", "no-store"); // single-use token: never cache
    return res.status(r.status).json(payload);
  } catch {
    return res.status(502).json({ error: "Calling is unavailable right now." });
  }
});

app.listen(3000);
```

### Go (net/http)

```go
// main.go. Run with: TRING_API_URL=https://tring.example.com TRING_API_KEY=... go run .
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

var (
	tringAPIURL = os.Getenv("TRING_API_URL") // e.g. https://tring.example.com
	tringAPIKey = os.Getenv("TRING_API_KEY") // tk_live_…, from your secret manager
	tringFlowID = os.Getenv("TRING_FLOW_ID") // optional; else the tenant default flow
	client      = &http.Client{Timeout: 10 * time.Second}
)

type user struct{ CustomerID string }

// Replace with your real session lookup. Returns nil if not logged in.
func currentUser(r *http.Request) *user { return nil }

// Replace with your CAPTCHA check / rate limiter for anonymous visitors.
func allowAnonymousCall(r *http.Request) bool { return false }

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func visitorCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if u == nil && !allowAnonymousCall(r) {
		writeErr(w, http.StatusUnauthorized, "Please sign in to call us.")
		return
	}

	body := map[string]string{}
	if tringFlowID != "" {
		body["flow_id"] = tringFlowID
	}
	if u != nil {
		body["client_ref"] = u.CustomerID // only a verified identity
	}
	buf, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		tringAPIURL+"/v1/guest-credentials", bytes.NewReader(buf))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "request build failed")
		return
	}
	req.Header.Set("Authorization", "Bearer "+tringAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Calling is unavailable right now.")
		return
	}
	defer resp.Body.Close()

	// Pass Tring's JSON (or its {"error": …}) straight through.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store") // single-use token: never cache
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

func main() {
	http.HandleFunc("/tring/visitor-credentials", visitorCredentials)
	log.Fatal(http.ListenAndServe(":3000", nil))
}
```

In production, run either example behind TLS, on the same site as the page that loads `voice.js`.

### If the endpoint is on another origin

The widget sends cookies with the request. If your credentials endpoint is on a different origin from the page, the endpoint must send `Access-Control-Allow-Origin: <your page's exact origin>` (not `*`) and `Access-Control-Allow-Credentials: true`, and your session cookie needs `SameSite=None; Secure`. It's simpler to serve the endpoint from the same origin as the page.

## Events and callbacks

The widget doesn't expose its session or any callbacks to the page. It drives its own UI. If you need to react to call events (for analytics, or to build your own UI), use `TringGuest` from the [JavaScript API](/tring/sdk/javascript-api/#tringguest) instead. It emits `connecting`, `ringing`, `connected`, `ended`, `remote-video` and `error`.

`voice.js` sets `window.TringGuest` when it loads, so you can use the class without a bundler. If you load `voice.js` with no `data-*` attributes to get only the class, the widget logs a harmless "provide data-gateway-url … or data-credentials-url" error to the console and renders nothing.

## Styling

The widget adds its CSS as `<style id="tring-guest-styles">` in `<head>`, **unless an element with that id already exists**. You can:

- **Override some rules.** Add your own CSS with higher specificity, or load it after the widget. For example:

  ```css
  .tring-guest-launcher { background: #0f766e; }
  .tring-guest-launcher:hover { background: #115e59; }
  ```

- **Replace all the styles.** Put your own `<style id="tring-guest-styles">…</style>` in the page before `voice.js` loads. The widget then adds no CSS of its own.

Hooks you can target:

| Selector | What it is |
|---|---|
| `.tring-guest-launcher` | The floating launcher button (fixed, bottom-right, `z-index: 999998`) |
| `#tring-guest-ui` | The full-screen call sheet (`z-index: 999999`). It's visible when it has the class `.tring-guest-visible`. |
| `#tring-guest-ui[data-state="…"]` | Call state: `idle`, `connecting`, `ringing`, `connected` or `ended`. The dialpad and the action buttons show only in `connected`. |
| `#tring-guest-ui.tring-guest-has-video` | Set while the agent is sharing their screen |
| `.tring-guest-card`, `.tring-guest-title`, `.tring-guest-sub`, `.tring-guest-avatar` | The parts of the call sheet |
| `.tring-guest-keypad`, `.tring-guest-key`, `.tring-guest-dialed` | The dialpad and the digits pressed so far |
| `.tring-guest-mute`, `.tring-guest-share`, `.tring-guest-end` | The action buttons. `.is-active` is set while mute or screen sharing is on. |
| `.tring-guest-remote` | The `<video>` element for the agent's shared screen |

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Nothing renders, and the console says "could not find own &lt;script&gt; tag" or "provide data-gateway-url … or data-credentials-url" | The `<script>` tag has neither attribute, or the bundle was added dynamically without them. |
| `/sdk/voice.js` returns 404 | The API was built without the bundles. Run `make sdk` and rebuild, or use the published image. |
| "Microphone needs HTTPS" | The page is on `http://` (other than `localhost`). Serve it over HTTPS. |
| "Microphone blocked", or the call ends right after you tap | The visitor denied mic permission. In an iframe, add `allow="microphone"`. |
| "credentials request failed (401)" (or your own error text) | Your credentials endpoint refused the request: no session, or the CAPTCHA/rate limit check failed. |
| "no flow for this guest call — pass flow_id, or set a default flow in Settings" | You didn't send `flow_id`, and the tenant has no default flow. |
| "flow not found or inactive" / "invalid flow_id" | The `flow_id` is wrong, belongs to another tenant, or the flow is inactive. |
| "signaling socket error" or "token rejected" | You're using `data-gateway-url` without a token, the token expired (2 minutes), or a token was used twice (for example, a cached response). Mint a new one per call and send `Cache-Control: no-store`. |
| Stuck on "Calling…", no audio | The browser can't reach the gateway over UDP. Check `TRING_GATEWAY_PUBLIC_URL` and `TRING_PUBLIC_IP`, and configure TURN for restrictive networks. |
| No ringback or audio until you tap | The browser blocked autoplay. The Call tap normally unlocks it. Make sure calls start from a real click. |

For a working setup, run [`examples/demo-portal/`](https://github.com/shridarpatil/tring/tree/main/examples/demo-portal). `/support` is an anonymous visitor with an inline button, and `/portal` is a logged-in visitor with `client_ref`. See also [Web calling](/tring/features/web-calling/).
