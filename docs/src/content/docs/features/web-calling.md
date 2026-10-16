---
title: Web calling
description: Browser calling for website visitors and agents over WebRTC to the Tring gateway, with backend-minted call tokens and page-refresh recovery.
---

Visitors call you from a web page ("call us"), and agents take calls in their browser. Both connect over plain WebRTC to the gateway's `/rtc` endpoint. There is no SIP and no FreeSWITCH in a browser call.

```
 Visitor browser ─┐                       ┌─ IVR (a participant)
                  ├─ WebSocket + WebRTC ─►│  room ──► routing ──► agent browser
 Agent browser  ──┘   gateway /rtc :8087  └─ recording, call log
```

Every call is a **room** on the gateway. The visitor, the IVR and the agent are all participants in it, so "connect to a human" means an agent joins the room and the IVR leaves.

## Who connects, with what

| Who | Token | Minted by | Lifetime | Use |
|---|---|---|---|---|
| Visitor | Gateway call token (`aud: tring.gateway`) | `POST /v1/guest-credentials`, called by **your backend** with an API key | 2 minutes, **single use** | Join one call, pinned to one IVR flow |
| Agent | Agent token (`aud: tring.sdk`) | `GET /v1/agents/{id}/token`, called by **your backend** | 5 minutes | Go online, get rung, join calls |
| Supervisor | Supervisor token (`aud: tring.supervisor`) | `POST /v1/calls/{id}/supervise`, console login only | 2 minutes, single use | Listen to, whisper on or barge into one live web call |

All three are HS256 JWTs signed with `TRING_JWT_SECRET`. The gateway accepts them on `/rtc` as `Authorization: Bearer <token>` or `?token=<token>`. A request with no valid token gets `401`.

:::danger
The browser never holds your API key. Your backend calls Tring with the key and passes only the short-lived token to the page. See [Multi-tenancy and auth](/tring/features/multi-tenancy-and-auth/).
:::

## Visitor calls

1. The visitor clicks **Call**. The [visitor widget](/tring/sdk/visitor-widget/) asks for the microphone, then fetches credentials from **your** endpoint (`data-credentials-url`).
2. Your endpoint decides whether to allow the call (session check, CAPTCHA, rate limit), then calls Tring:

   ```bash
   curl -sS -X POST https://api.example.com/v1/guest-credentials \
     -H "Authorization: Bearer $TRING_API_KEY" \
     -H "Content-Type: application/json" \
     -d '{"flow_id":"<flow-uuid>","client_ref":"crm-customer-1042"}'
   ```

   Both fields are optional:

   | Field | What it does |
   |---|---|
   | `flow_id` | The IVR flow to run. Must belong to your tenant and be active. Omit it to use the tenant's **default flow** (Settings, or `PATCH /v1/guest-config`). With neither, the request fails with `400`. |
   | `client_ref` | Your verified identity for the caller, for example a CRM customer id. It is stored on the call log and passed to the IVR as the `caller` variable. Omit it for anonymous visitors. |

3. Tring returns everything the browser needs:

   ```json
   {
     "gateway_url": "https://gw.example.com",
     "flow_id": "<flow-uuid>",
     "token": "<gateway call token>",
     "ice_servers": [{ "urls": "turns:turn.example.com:443", "username": "…", "credential": "…" }]
   }
   ```

4. The browser opens `/rtc` with the token and joins a room. The gateway **burns the token's id in Redis** on this first join, so a captured token cannot start a second call. The flow comes from the token, never from the browser, so a visitor cannot pick a different flow.
5. The gateway opens a call record (source `web`), starts the IVR, and routes the call to agents as the flow says.

`client_ref` is trusted because only a holder of your API key can mint the token. Pass only identities your backend has verified.

## Agent calls

1. Your backend mints an agent token with the API key:

   ```bash
   curl -sS https://api.example.com/v1/agents/<agent-id-or-sip-username>/token \
     -H "Authorization: Bearer $TRING_API_KEY"
   ```

   The response is `{ token, expires_at, gateway_url, ice_servers }`. The path accepts the agent UUID or its `sip_username`, so you can key agents by your own user id.

2. Your page loads the [agent widget](/tring/sdk/agent-widget/) (or the [JavaScript API](/tring/sdk/javascript-api/)) pointing at your token endpoint. It opens a **presence** socket to `/rtc` and registers as online. Presence is kept in Redis and refreshed every 10 seconds; an agent that disconnects drops out within 30 seconds.
3. When routing picks the agent, the gateway pushes an `incoming` message with a caller label. The agent accepts by joining the call's room on a second socket, or declines (the router moves to the next agent at once).
4. While joined, the agent can hold, resume, transfer and send DTMF. See [Call control](/tring/features/call-control/).

The token is only used to open sockets, so a 5-minute lifetime is enough; the widget fetches a fresh one from your endpoint when it reconnects. Agents cannot place calls from the browser through the SDK; outbound calls go through your backend (see [Outbound calls](/tring/features/outbound-calls/)).

## Page-refresh grace

When a participant's connection drops (page refresh, network blip), the gateway does not end the call. It holds the participant's slot for `TRING_MEDIA_GRACE_SECONDS` (default 30).

- On join, the gateway sends the client a `session_token`. The SDK keeps it and sends it in the next `join` to **reattach** to the same slot, so the other side keeps hearing the call and the call record continues.
- A reattach does not burn a new visitor token and does not open a new call record.
- If nobody reattaches in time, the participant is removed. If only the caller is left in a web room, the call ends.
- An explicit hangup (`bye`) ends the participant's part immediately; there is no grace for it.

## Media and network

- Audio is Opus. The gateway is the only offerer; the browser answers.
- Browser media uses UDP ports `TRING_GATEWAY_UDP_MIN`–`TRING_GATEWAY_UDP_MAX` on `TRING_PUBLIC_IP`. Open that range.
- For UDP-blocked networks, configure TURN with `TRING_TURN_SECRET` so each token response carries short-lived TURN credentials. See [Configuration](/tring/getting-started/configuration/#7-webrtc-ice-stun-and-turn).
- Browsers only allow the microphone on secure origins. Serve your pages, the api and the gateway over **HTTPS** (`wss://` for `/rtc`) in production.

## What's next

- [Visitor widget](/tring/sdk/visitor-widget/) and [agent widget](/tring/sdk/agent-widget/) embed snippets
- [IVR flows](/tring/features/ivr-flows/) to decide what a caller hears
- [Agents and routing](/tring/features/agents-and-routing/) to decide who answers
