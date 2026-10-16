---
title: Webhooks
description: Register webhook endpoints, the events and exact payloads Tring sends, how to verify the Tring-Signature header, and the retry schedule.
---

Webhooks let Tring push events to your backend over HTTP. You register a URL, optionally limited to some event types, and Tring POSTs a signed JSON body to it when those events happen. For the concepts, see [Webhooks](/tring/features/webhooks/).

All management routes take a tenant API key or a console session. See [Authentication](/tring/api-reference/authentication/).

## Events

| Event | Sent when | Sent by |
|---|---|---|
| `call.ringing` | Your backend places a call with [`POST /v1/calls`](/tring/api-reference/calls/#post-v1calls) or [`POST /v1/calls/connect`](/tring/api-reference/calls/#post-v1callsconnect). | API |
| `agent.status` | An agent's availability changes through `POST /v1/agents/{id}/availability`. | API |

:::caution
`call.answered` and `call.ended` are defined as event names, and the console lets you subscribe to them, but the current code never sends them as webhooks. Inbound, web and IVR calls don't send `call.ringing` either. For live call state, use the dashboard event stream (`GET /v1/stream/dashboard`) or poll [`GET /v1/calls`](/tring/api-reference/calls/#get-v1calls) and [`GET /v1/calls/{id}/events`](/tring/api-reference/calls/#get-v1callsidevents).
:::

## The request Tring sends

```http
POST /tring/webhook HTTP/1.1
Host: crm.example.com
Content-Type: application/json
User-Agent: Tring-Webhook/0.1
Tring-Event: call.ringing
Tring-Delivery: 2f9d1c3a-8888-4b00-9c00-000000000001
Tring-Signature: t=1792134165,v1=5d1f0c…(64 hex characters)
```

| Header | Value |
|---|---|
| `Tring-Event` | The event type. |
| `Tring-Delivery` | Delivery ID. The same across every retry of one delivery, so use it to de-duplicate. |
| `Tring-Signature` | `t=<unix seconds>,v1=<hex HMAC-SHA256>`. See [Verifying signatures](#verifying-signatures). |

The body is a JSON envelope:

| Field | Type | Description |
|---|---|---|
| `event` | string | Event type, same as `Tring-Event`. |
| `delivery_id` | UUID | Same as `Tring-Delivery`. |
| `created` | timestamp | When the envelope was built, in UTC. |
| `data` | object | Event-specific payload. |

### `call.ringing`

`data.call` is the [call object](/tring/api-reference/calls/#the-call-object) as returned by the call that created it. Fields with no value yet (`answered_at`, `ended_at`, `agent_name`, …) are left out. `agent_id` is present for `POST /v1/calls` and absent for `POST /v1/calls/connect`.

```json
{
  "event": "call.ringing",
  "delivery_id": "2f9d1c3a-8888-4b00-9c00-000000000001",
  "created": "2026-10-16T05:32:45.204117Z",
  "data": {
    "call": {
      "id": "c0ffee00-6666-4a00-8f00-000000000001",
      "tenant_id": "7d3e9f10-0000-4000-8000-000000000001",
      "direction": "outbound",
      "status": "ringing",
      "from_number": "+919800000001",
      "to_number": "+919800000003",
      "agent_id": "a1b2c3d4-0000-4000-8000-000000000001",
      "source": "phone",
      "leg_id": "6b8d2f10-7777-4c00-8a00-000000000001",
      "started_at": "2026-10-16T11:02:45.118204+05:30"
    }
  }
}
```

### `agent.status`

```json
{
  "event": "agent.status",
  "delivery_id": "2f9d1c3a-8888-4b00-9c00-000000000002",
  "created": "2026-10-16T05:40:02.871300Z",
  "data": {
    "agent_id": "a1b2c3d4-0000-4000-8000-000000000001",
    "sip_username": "agent-one",
    "available_for_calls": false
  }
}
```

| Field | Type | Description |
|---|---|---|
| `agent_id` | UUID | The agent. |
| `sip_username` | string | The agent's SIP username (often your own CRM user ID). |
| `available_for_calls` | boolean | The new availability. |

## Verifying signatures

Every request is signed with the webhook's secret, using the same scheme as Stripe:

```
signed_payload = "<t>" + "." + <raw request body>
v1             = hex( HMAC-SHA256( secret, signed_payload ) )
```

To verify:

1. Read the **raw** body bytes before parsing JSON. Re-serialising parsed JSON changes the bytes and breaks the signature.
2. Split `Tring-Signature` on `,` and take the `t` and `v1` values.
3. Reject the request if `t` is too far from your clock, for example more than 5 minutes. `t` is part of the signed material, so this stops replays.
4. Compute the HMAC yourself and compare it to `v1` in **constant time**.
5. Only then parse and act on the body. Return `2xx` quickly and do slow work afterwards.

Keep the secret in an environment variable or secret manager.

**Go**

```go
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const tolerance = 5 * time.Minute

// Verify checks the Tring-Signature header and returns the raw body.
func Verify(r *http.Request) ([]byte, error) {
	secret := os.Getenv("TRING_WEBHOOK_SECRET")
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var ts, sig string
	for _, part := range strings.Split(r.Header.Get("Tring-Signature"), ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	if ts == "" || sig == "" {
		return nil, errors.New("missing signature")
	}

	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return nil, errors.New("bad timestamp")
	}
	if d := time.Since(time.Unix(unix, 0)); d > tolerance || d < -tolerance {
		return nil, errors.New("timestamp outside tolerance")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want := mac.Sum(nil)

	got, err := hex.DecodeString(sig)
	if err != nil || !hmac.Equal(got, want) {
		return nil, errors.New("signature mismatch")
	}
	return body, nil
}
```

**Node.js** (built-in `crypto` and `http`, no packages)

```js
const crypto = require("node:crypto");
const http = require("node:http");

const SECRET = process.env.TRING_WEBHOOK_SECRET;
const TOLERANCE_SECONDS = 300;

function verify(rawBody, header) {
  const parts = Object.fromEntries(
    (header || "").split(",").map((p) => p.trim().split("=", 2))
  );
  const ts = parts.t;
  const sig = parts.v1;
  if (!ts || !sig) return false;

  const age = Math.abs(Date.now() / 1000 - Number(ts));
  if (!Number.isFinite(age) || age > TOLERANCE_SECONDS) return false;

  const expected = crypto
    .createHmac("sha256", SECRET)
    .update(ts + ".")
    .update(rawBody)
    .digest();
  const given = Buffer.from(sig, "hex");
  return given.length === expected.length && crypto.timingSafeEqual(given, expected);
}

http
  .createServer((req, res) => {
    const chunks = [];
    req.on("data", (c) => chunks.push(c));
    req.on("end", () => {
      const raw = Buffer.concat(chunks);
      if (!verify(raw, req.headers["tring-signature"])) {
        res.writeHead(401).end();
        return;
      }
      const event = JSON.parse(raw.toString("utf8"));
      res.writeHead(204).end(); // acknowledge first, then process
      // handle event.event / event.data here, de-duplicating on event.delivery_id
    });
  })
  .listen(3000);
```

If you use a framework, make sure you get the raw body: in Express, for example, use `express.raw({ type: "application/json" })` on the webhook route rather than `express.json()`.

## Delivery and retries

- A delivery succeeds on any `2xx` response. Anything else, including a timeout or connection error, is a failure.
- Each attempt times out after **10 seconds**.
- After a failure Tring waits and retries: **1, 2, 4, 8 and 16 seconds** between attempts. That's 6 attempts in total, over about 31 seconds plus time spent waiting on responses.
- After the last failed attempt the delivery is marked `exhausted`. There's no automatic redelivery after that, and no replay endpoint yet.
- Each attempt is signed afresh, so `t` (and therefore `v1`) changes between retries. `Tring-Delivery` stays the same.
- Retries run in the memory of the process that sent the event. If it restarts mid-retry, the delivery stays `pending` and isn't resumed.
- Deliveries for different events run independently, so they can arrive out of order. Use timestamps in the payload, not arrival order.

Your endpoint should be idempotent: de-duplicate on `Tring-Delivery`.

### POST /v1/webhooks

Register a webhook endpoint.

**Auth:** tenant API key or console session.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `url` | string | yes | Where to POST events. Not validated, so check it yourself; use `https://`. |
| `events` | array of strings | no | Event types to receive. Empty or omitted means **all** events. Values aren't validated, so a typo silently receives nothing. |
| `secret` | string | no | Signing secret. When omitted, Tring generates one (`whsec_` followed by 48 hex characters). |

```bash
curl -sS -X POST "$TRING_URL/v1/webhooks" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://crm.example.com/tring/webhook", "events": ["call.ringing", "agent.status"]}'
```

**Response:** `201 Created`. This is the **only** response that includes `secret`. Store it in your secret manager now.

```json
{
  "id": "e4a1b2c3-9999-4d00-8b00-000000000001",
  "tenant_id": "7d3e9f10-0000-4000-8000-000000000001",
  "url": "https://crm.example.com/tring/webhook",
  "secret": "whsec_***REDACTED***",
  "events": ["call.ringing", "agent.status"],
  "created_at": "2026-10-16T11:20:00.120044+05:30"
}
```

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid JSON body` |
| `400` | `url is required` |
| `500` | `insert webhook: …` |

:::caution
Only register HTTPS endpoints you control. Tring sends to whatever URL is registered, including private and internal addresses reachable from the API server, so operators should restrict who can hold API keys and consider egress filtering. Treat the secret like a password: if it leaks, delete the webhook and create a new one. There's no way to rotate a secret in place.
:::

### GET /v1/webhooks

List your tenant's webhooks, newest first. Secrets are never included.

**Auth:** tenant API key or console session.

```bash
curl -sS "$TRING_URL/v1/webhooks" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `200 OK`

```json
[
  {
    "id": "e4a1b2c3-9999-4d00-8b00-000000000001",
    "tenant_id": "7d3e9f10-0000-4000-8000-000000000001",
    "url": "https://crm.example.com/tring/webhook",
    "events": ["call.ringing", "agent.status"],
    "created_at": "2026-10-16T11:20:00.120044+05:30"
  }
]
```

**Errors**

| Status | Message |
|---|---|
| `500` | `query failed` / `scan failed` |

### DELETE /v1/webhooks/{id}

Delete a webhook. Its delivery history is deleted with it.

**Auth:** tenant API key or console session.

```bash
curl -sS -X DELETE "$TRING_URL/v1/webhooks/e4a1b2c3-9999-4d00-8b00-000000000001" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `204 No Content`.

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid id` |
| `404` | `webhook not found` |
| `500` | `delete failed` |

### GET /v1/webhooks/{id}/deliveries

The 100 most recent deliveries to a webhook, newest first. Useful while you build your endpoint.

**Auth:** tenant API key or console session.

```bash
curl -sS "$TRING_URL/v1/webhooks/e4a1b2c3-9999-4d00-8b00-000000000001/deliveries" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `200 OK`

| Field | Type | Description |
|---|---|---|
| `id` | UUID | Delivery ID (the `Tring-Delivery` header). |
| `webhook_id` | UUID | |
| `event_type` | string | |
| `status` | string | `pending` (still trying), `success` or `exhausted` (gave up). |
| `attempts` | integer | Attempts made so far. |
| `last_error` | string | Last failure, for example `non-2xx: 500` or a connection error. Omitted when none. |
| `response_status` | integer | Last HTTP status received. Omitted when no response was received. |
| `delivered_at` | timestamp | When it succeeded. Omitted otherwise. |
| `created_at` | timestamp | |

```json
[
  {
    "id": "2f9d1c3a-8888-4b00-9c00-000000000001",
    "webhook_id": "e4a1b2c3-9999-4d00-8b00-000000000001",
    "event_type": "call.ringing",
    "status": "success",
    "attempts": 2,
    "response_status": 204,
    "delivered_at": "2026-10-16T11:02:46.402190+05:30",
    "created_at": "2026-10-16T11:02:45.130551+05:30"
  }
]
```

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid id` |
| `404` | `webhook not found` |
| `500` | `lookup failed` / `query failed` / `scan failed` |
