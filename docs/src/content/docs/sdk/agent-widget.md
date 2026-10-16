---
title: Agent widget (agent.js)
description: Keep agents online inside your CRM and give them a call panel for ringing, answering, hold, transfer, DTMF and screen sharing.
---

`agent.js` is a drop-in call panel for agents. It connects the agent to the Tring media gateway, keeps them online, and shows a panel whenever a call rings them: an IVR transfer, a web visitor, or another agent's transfer.

The widget **cannot place calls**. It has no access to the Tring API for starting calls. Outbound calls are placed by your backend (see [Placing outbound calls](#placing-outbound-calls)), so the agent's browser never sees the number it is calling.

The widget renders:

- a call panel, bottom-right. While a call rings, it shows **Accept** and **Decline**. During a call, it shows a timer and buttons for **Mute**, **Keypad** (DTMF), **Share screen**, **Hold**, **Transfer** and **Hang up**.
- a connection badge, bottom-left: **Online**, **Reconnecting…** or **Offline**
- a ring tone for incoming calls, the caller's shared screen when they share it, and a "Supervising" or "Whisper" badge when a supervisor joins the call

## Add it to a page

```html
<script src="https://tring.example.com/sdk/agent.js"
        data-token-url="/tring/agent-token"></script>
```

`/tring/agent-token` is an endpoint on **your** backend. It checks that the agent is logged in, mints a token with your API key, and returns the JSON. The response also includes the gateway URL and ICE servers, so that one attribute is usually all you need.

## Attributes

| Attribute | Meaning |
|---|---|
| `data-token-url` | Your endpoint that returns `{ token, gateway_url?, ice_servers? }` for the logged-in agent. The widget fetches it with GET, with same-origin cookies (`credentials: "same-origin"`): once at start-up, again on every reconnect, and again before each transfer-list request. **Recommended.** |
| `data-token` | A static agent JWT, for example rendered into a server-side page. It can't be refreshed: once it expires (5 minutes), reconnects fail until the page reloads. Use it with `data-gateway-url`. |
| `data-gateway-url` | Gateway base URL, for example `https://gw.tring.example.com`. Optional with `data-token-url` (the token response carries `gateway_url`); required with `data-token`. If set, it overrides the response. |
| `data-api-base` | Tring API base URL, used for the transfer target list. By default the widget derives it from its own `src` (it strips `/sdk/agent.js`). Set it only if you load `agent.js` from somewhere other than your Tring API, such as your own CDN. |

The `<script>` tag needs `data-token` or `data-token-url`. Without either, the widget logs an error to the console and doesn't start.

## Getting the agent token

Your backend calls Tring with your tenant API key:

```http
GET /v1/agents/{id}/token
Authorization: Bearer <tenant API key>
```

`{id}` can be the agent's UUID **or** their `sip_username`. If you set an agent's `sip_username` to your CRM user id, you can mint tokens without looking up the UUID first. The response:

```json
{
  "token": "eyJ…",
  "expires_at": "2026-01-01T12:05:00Z",
  "gateway_url": "https://gw.tring.example.com",
  "ice_servers": [{ "urls": ["stun:stun.example.com:3478"] }]
}
```

Pass it through to the browser unchanged. The token is valid for **5 minutes**. You don't need to refresh it on a timer: the widget fetches a new one from `data-token-url` every time it reconnects. See [Agents API](/tring/api-reference/agents/).

:::danger
The token endpoint must check **your** agent session, and mint a token only for the agent who is logged in, never for an id taken from the request. Keep the tenant API key in an environment variable or a secret manager on the server.
:::

The widget fetches with `credentials: "same-origin"`, so it sends cookies only if the token endpoint is on the **same origin as the page**. Host it on your CRM's own origin.

## Taking calls

1. When the agent's page loads, the widget fetches a token, connects to the gateway, and shows **Online**. It also asks for the microphone now, so later calls can be answered without a new permission prompt. If the agent denies it here, the widget asks again when they accept a call.
2. When a call rings this agent, the panel slides in with the caller's label and **Accept** / **Decline**, and plays a ring tone.
   - **Accept** joins the call. The panel shows "Connecting…", then a timer once audio is flowing.
   - **Decline** rejects the call. Tring moves on to the next agent straight away instead of waiting for the ring timeout.
   - If another agent answers first, or the team's ring strategy moves to the next agent, the panel closes by itself.
3. **Hang up** ends the call. The panel also closes when the other party hangs up or the IVR ends the call.

The widget always asks the agent to Accept or Decline. It doesn't answer calls automatically. To answer automatically, use the [JavaScript API](/tring/sdk/javascript-api/#tring) with `autoAnswer: true`.

The caller's label comes from your backend (for example `agent_label` on an outbound call). It is never the caller's phone number. If no label is set, the panel shows "Unknown caller".

### In-call controls

| Button | What it does |
|---|---|
| Mute | Stops sending the agent's microphone. This only affects the agent's browser. |
| Keypad | Opens a dialpad. Each digit goes to the call's IVR over the signaling socket. |
| Share screen | Shares the agent's screen or a window with the caller, after the browser's picker. Click again to stop. |
| Hold / Resume | Hold plays the tenant's hold music to the caller and mutes the agent. Resume reverses both. The button updates immediately; there is no confirmation from the server. |
| Transfer | Opens a list of online agents and teams. Choosing one starts a blind transfer, and the agent's own call ends when the target answers. |
| Hang up | Ends the call. |

### How hold and transfer work

The widget sends **hold**, **resume** and **transfer** over its call socket to the gateway (`/rtc`). Hold music is played on the server, so no API call is needed for them.

The API is used only for the **transfer list**. When the agent opens Transfer, the widget calls:

```http
GET https://tring.example.com/v1/sdk/transfer-targets
Authorization: Bearer <agent JWT>
```

```json
{
  "agents": [{ "sip": "agent-2002", "name": "Agent B" }],
  "teams": ["support", "billing"]
}
```

`agents` lists the tenant's other agents who are online and available (the agent never appears in their own list). `teams` lists every team name. The widget gets a fresh agent token from `data-token-url` for each request.

:::note
If the widget can't work out an API base (no `data-api-base`, and its `src` doesn't end in `/sdk/agent.js`), the Transfer button does nothing. If the transfer target turns out to be unavailable, the server keeps the call with the current agent, but the widget doesn't currently show that the transfer failed.
:::

### Agent-JWT call-control endpoints

The API also has REST endpoints that accept the **agent JWT** (not the API key) and act on that agent's own active call. The widget only uses `transfer-targets`. Your own UI can call the others directly:

| Method | Path | Body | Success | Errors |
|---|---|---|---|---|
| `POST` | `/v1/sdk/calls/hold` | none | `200 {"held": true}` | `401` invalid agent token, `409` no active call / no media leg yet, `502` hold command failed |
| `POST` | `/v1/sdk/calls/resume` | none | `200 {"held": false}` | same as hold |
| `GET` | `/v1/sdk/transfer-targets` | none | `200 {"agents": [...], "teams": [...]}` | `401` |
| `POST` | `/v1/sdk/calls/transfer` | `{"to_sip": "…"}` or `{"to_team": "…"}` | `200 {"transferred": true}` | `400` missing target / transfer to yourself, `404` unknown target agent, `409` target unavailable / no available team members / no active call, `502` |

These endpoints look up the agent's active **FreeSWITCH** call leg. A call that runs entirely on the WebRTC media plane (a web visitor talking to an agent) may have no such leg, and then returns `409`. For those calls, use the widget's buttons or the `Call` methods in the [JavaScript API](/tring/sdk/javascript-api/#call). To control any call by id from your backend, use the API-key endpoints `POST /v1/calls/{id}/hold` and `/resume` (see [Call control](/tring/features/call-control/)).

## Placing outbound calls

The agent clicks **Call** in your CRM. Your page sends a **customer id** to your backend. Your backend looks up the number and asks Tring to place the call:

```http
POST /v1/calls
Authorization: Bearer <tenant API key>
Content-Type: application/json

{ "agent_id": "<agent UUID>", "to": "<number>", "agent_label": "Ticket #4821" }
```

The agent's call then rings their widget, and the panel shows only `agent_label`. See [Calls API](/tring/api-reference/calls/) and [Outbound calls & masking](/tring/features/outbound-calls/).

:::caution
On the WebRTC media plane, backend click-to-call to a **phone number** isn't fully wired yet, according to the SDK source. Today the widget mainly receives IVR transfers, web visitors, and WhatsApp calls. Test outbound calling on your deployment before relying on it.
:::

## Events

The widget doesn't expose its `Tring` instance or any callbacks to the page. It sets `window.Tring` to the class, but calling `Tring.init` alongside the widget opens a **second** presence connection for the same agent. If you need call events in your own code (for example, to open the customer's record when a call is answered), drop the widget and build your panel on the [JavaScript API](/tring/sdk/javascript-api/#tring), which emits `registered`, `unregistered`, `reconnecting`, `call:ringing`, `call:answered`, `call:ended`, `call:remote-video`, `call:supervisor` and `error`.

## Staying online across page navigation

The agent's connection and any active call live in the page's JavaScript. A full page load, a hard reload or a normal link to a new URL **drops the active call**. After the load, the widget connects again and the agent shows as online after a short gap, but the call is gone. Client-side routing in a single-page app (Vue Router, React Router, …) keeps both the connection and the call.

If your CRM reloads pages on every click, host the widget somewhere that doesn't reload: a persistent iframe (with `allow="microphone; display-capture"`) or a pop-out window, like a classic dialer toolbar.

On short network drops (Wi-Fi blips, gateway redeploys, an expired token), the widget reconnects by itself, backing off from 1 second up to 10 seconds and fetching a fresh token each time. A call in progress gets up to 3 attempts to rejoin.

## Agent availability

The badge shows only whether the **connection** is up. Whether the agent is Away or on a break is up to your CRM: set it from your backend with `POST /v1/agents/{id}/availability` and `{"available": false}`. An unavailable agent stays connected but isn't rung. See [Agents API](/tring/api-reference/agents/).

## Complete CRM example

This example is one Go file that serves a CRM page with the widget, a token endpoint, and a click-to-call endpoint. It uses only the standard library. The session lookup is a placeholder for your real auth.

```go
// crm.go. Run with:
//   TRING_API_URL=https://tring.example.com TRING_API_KEY=... go run crm.go
package main

import (
	"bytes"
	"encoding/json"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

var (
	tringAPIURL = os.Getenv("TRING_API_URL") // e.g. https://tring.example.com
	tringAPIKey = os.Getenv("TRING_API_KEY") // tk_live_…, from your secret manager
	client      = &http.Client{Timeout: 10 * time.Second}
)

// Your CRM's records. Phone numbers stay on the server.
type customer struct{ Name, Number, Label string }

var customers = map[string]customer{
	"c1": {"Customer A", "+15550100001", "Customer A · #4821"},
	"c2": {"Customer B", "+15550100002", "Customer B · #4822"},
}

type agentSession struct {
	TringAgentID string // the agent's Tring UUID (or use their sip_username for the token call)
}

// Replace with your real session lookup (cookie → logged-in agent).
func currentAgent(r *http.Request) *agentSession { return nil }

// tring calls the Tring API with the tenant API key and relays the response.
func tring(w http.ResponseWriter, r *http.Request, method, path string, body any) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, tringAPIURL+path, rd)
	if err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+tringAPIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, `{"error":"tring unreachable"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

// GET /tring/agent-token, the agent.js data-token-url.
func agentToken(w http.ResponseWriter, r *http.Request) {
	a := currentAgent(r)
	if a == nil {
		http.Error(w, `{"error":"not signed in"}`, http.StatusUnauthorized)
		return
	}
	tring(w, r, http.MethodGet, "/v1/agents/"+a.TringAgentID+"/token", nil)
}

// POST /crm/call {"customer":"c1"}. The browser sends an id, never a number.
func placeCall(w http.ResponseWriter, r *http.Request) {
	a := currentAgent(r)
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if a == nil {
		http.Error(w, `{"error":"not signed in"}`, http.StatusUnauthorized)
		return
	}
	var in struct{ Customer string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	c, ok := customers[in.Customer]
	if !ok {
		http.Error(w, `{"error":"unknown customer"}`, http.StatusBadRequest)
		return
	}
	tring(w, r, http.MethodPost, "/v1/calls", map[string]string{
		"agent_id": a.TringAgentID, "to": c.Number, "agent_label": c.Label,
	})
}

var page = template.Must(template.New("crm").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>CRM</title></head>
<body>
  <h1>Customers</h1>
  <ul>
  {{range $id, $c := .}}
    <li>{{$c.Name}} <button data-customer="{{$id}}">Call</button></li>
  {{end}}
  </ul>

  <script>
    // Your own Call buttons hit YOUR backend. The widget never places calls.
    document.querySelectorAll("button[data-customer]").forEach((b) =>
      b.addEventListener("click", () =>
        fetch("/crm/call", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ customer: b.dataset.customer }),
        })));
  </script>

  <script src="https://tring.example.com/sdk/agent.js"
          data-token-url="/tring/agent-token"></script>
</body></html>`))

func crm(w http.ResponseWriter, r *http.Request) {
	if currentAgent(r) == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// Only names and ids reach the page, never numbers.
	view := map[string]struct{ Name string }{}
	for id, c := range customers {
		view[id] = struct{ Name string }{c.Name}
	}
	_ = page.Execute(w, view)
}

func main() {
	http.HandleFunc("/crm", crm)
	http.HandleFunc("/tring/agent-token", agentToken)
	http.HandleFunc("/crm/call", placeCall)
	log.Fatal(http.ListenAndServe(":3000", nil))
}
```

Run it behind TLS in production: agents need HTTPS for microphone access. Protect `/crm/call` against cross-site request forgery (CSRF) the same way you protect your CRM's other state-changing endpoints, for example with a CSRF token or `SameSite` session cookies.

## Styling

The widget adds its CSS as `<style id="tring-styles">` in `<head>`, **unless an element with that id already exists**. To replace all the styles, put your own `<style id="tring-styles">` in the page first. To change some of them, override these selectors:

| Selector | What it is |
|---|---|
| `#tring-call-panel` | The panel (fixed bottom-right, `z-index: 999999`). It's visible when it has the class `.tring-panel-visible`. |
| `#tring-call-panel[data-state="…"]` | `idle`, `incoming`, `connecting`, `connected` or `error` |
| `.tring-panel-to`, `.tring-panel-sub`, `.tring-panel-avatar` | Caller label, status/timer line, avatar |
| `.tring-ctl` (with `.tring-accept`, `.tring-decline`, `.tring-mute`, `.tring-keypad`, `.tring-share`, `.tring-hold`, `.tring-transfer`, `.tring-hangup`) | Round control buttons. `.is-active` marks a toggled button. |
| `.tring-panel-dialpad`, `.tring-dtmf`, `.tring-panel-dialed` | Dialpad, its keys, and the digits pressed so far |
| `.tring-panel-picker`, `.tring-picker-item` | Transfer list |
| `.tring-panel-supervisor` | Supervisor badge |
| `.tring-status-badge[data-state="…"]` (`online`, `reconnecting`, `offline`) | Connection badge (fixed bottom-left, `z-index: 999998`) |

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Nothing renders, and the console says "add data-token or data-token-url" | The `<script>` tag has neither attribute. |
| "tring widget: token endpoint … returned 401" | Your token endpoint didn't see the agent's session. Make sure it's on the same origin as the page, so cookies are sent. |
| "gateway URL missing" | You used `data-token` without `data-gateway-url`, or your endpoint dropped `gateway_url` from Tring's response. |
| The badge keeps switching between Online and Reconnecting | The gateway isn't reachable over WSS, or `data-token` expired (it can't be refreshed). Use `data-token-url`. |
| Panel stuck on "Connecting…" after Accept | The microphone was denied, or media can't reach the gateway over UDP. Check permissions, `TRING_PUBLIC_IP`, and TURN. |
| The call drops when the agent clicks a link in the CRM | A full page load. See [Staying online across page navigation](#staying-online-across-page-navigation). |
| Transfer does nothing | The widget has no API base. Set `data-api-base`. |
| No ring tone | The browser blocked audio before the agent interacted with the page. The panel still appears. |

For a running example, see `/crm` in [`examples/demo-portal/`](https://github.com/shridarpatil/tring/tree/main/examples/demo-portal).
