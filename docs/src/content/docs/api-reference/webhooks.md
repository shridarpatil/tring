---
title: Webhooks
description: Register webhooks for IVR flows, the ivr.node and call.ended payloads, how to verify the Tring-Signature header, and the retry schedule.
---

A webhook is a registered URL, with optional headers and an active/disabled status. IVR flows pick webhooks from this registry: a node's **Notify webhook** sends `ivr.node` when the step finishes, and a flow's **On call end** webhook sends `call.ended` when a call through that flow ends. Tring POSTs a signed JSON body. For the concepts, see [Webhooks](/tring/features/webhooks/).

All management routes take a tenant API key with the `full` scope, or a console session. See [Authentication](/tring/api-reference/authentication/).

## Events

| Event | Sent when | Picked in |
|---|---|---|
| `ivr.node` | An IVR step that names a webhook finishes. A transfer sends it when someone answers (`outcome: "answered"`) or nobody does. | A node's **Notify webhook** (`config.webhook_id`) |
| `call.ended` | A call through the flow ends, however it ended: the caller hung up in the IVR or queue, after an agent, or the flow ended the call. | The flow's **On call end** (`end_webhook_id` in the definition) |

A disabled or deleted webhook is skipped silently. Both events are sent by the gateway, in the background, so they never delay the call.

:::caution
- `call.ended` has no `recording_url`: the recording is merged after the call ends. Fetch it later with [`GET /v1/calls/{id}/recording`](/tring/api-reference/calls/).
- Calls your backend places with [`POST /v1/calls`](/tring/api-reference/calls/#post-v1calls) or `/v1/calls/connect` don't run an IVR flow, so they send no webhooks. Poll [`GET /v1/calls/{id}`](/tring/api-reference/calls/#get-v1callsid) for their outcome.
- For live state (routing, ringing agents, availability), use the dashboard event stream (`GET /v1/stream/dashboard`) or [`GET /v1/calls/{id}/events`](/tring/api-reference/calls/#get-v1callsidevents).
:::

## The request Tring sends

```http
POST /tring/webhook HTTP/1.1
Host: crm.example.com
Content-Type: application/json
User-Agent: Tring-Webhook/0.1
Authorization: Bearer crm-token
Tring-Event: ivr.node
Tring-Delivery: 2f9d1c3a-8888-4b00-9c00-000000000001
Tring-Signature: t=1792134165,v1=5d1f0c…(64 hex characters)
```

| Header | Value |
|---|---|
| Your headers | Every header registered on the webhook (here `Authorization`). They can't override the headers below. |
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

### `ivr.node`

```json
{
  "event": "ivr.node",
  "delivery_id": "2f9d1c3a-8888-4b00-9c00-000000000001",
  "created": "2026-10-24T05:32:45.204117Z",
  "data": {
    "call_id": "c0ffee00-6666-4a00-8f00-000000000001",
    "flow_id": "f1f1f1f1-0000-4000-8000-000000000001",
    "node_id": "n_account",
    "node_type": "gather",
    "node_label": "Account number",
    "outcome": "default",
    "dtmf": "40213",
    "vars": { "caller": "+919800000001", "did": "+912265915000", "account": "40213" }
  }
}
```

| Field | Type | Description |
|---|---|---|
| `call_id` | UUID | The call ([`GET /v1/calls/{id}`](/tring/api-reference/calls/#get-v1callsid)). |
| `flow_id` | UUID | The flow the call is running. |
| `node_id`, `node_type`, `node_label` | string | The step that sent it. |
| `outcome` | string | The step's result: the edge the flow takes next (`default`, `digit:2`, `timeout`, `in_hours`, `http:2xx`, `no_answer`, …). A transfer sends `answered` when someone picks up. |
| `dtmf` | string | Keys the caller pressed in this step: the menu choice, or the digits a gather collected. Empty otherwise. |
| `vars` | object | The flow's variables so far: `caller`, `did`, gathered values and fields from HTTP callbacks. |

### `call.ended`

`data.call` is the [call object](/tring/api-reference/calls/#the-call-object) as `GET /v1/calls/{id}` returns it once the call has settled: `status: "ended"`, `duration_seconds`, `hangup_cause` and the agent, if one answered.

```json
{
  "event": "call.ended",
  "delivery_id": "2f9d1c3a-8888-4b00-9c00-000000000002",
  "created": "2026-10-24T05:40:02.871300Z",
  "data": {
    "call": {
      "id": "c0ffee00-6666-4a00-8f00-000000000001",
      "tenant_id": "7d3e9f10-0000-4000-8000-000000000001",
      "direction": "inbound",
      "status": "ended",
      "from_number": "+919800000001",
      "to_number": "+912265915000",
      "agent_id": "a1b2c3d4-0000-4000-8000-000000000001",
      "agent_name": "Agent One",
      "source": "phone",
      "duration_seconds": 217,
      "hangup_cause": "agent",
      "started_at": "2026-10-24T11:06:25.118204+05:30",
      "answered_at": "2026-10-24T11:06:51.402190+05:30",
      "ended_at": "2026-10-24T11:10:02.861120+05:30"
    }
  }
}
```

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
- Retries run in the gateway's memory. If it restarts mid-retry, the delivery stays `pending` and isn't resumed.
- Deliveries for different events run independently, so they can arrive out of order. Use timestamps in the payload, not arrival order.

Your endpoint should be idempotent: de-duplicate on `Tring-Delivery`.

### POST /v1/webhooks

Register a webhook. Then pick it in a flow: a node's **Notify webhook**, or the flow's **On call end**.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `url` | string | yes | Where to POST. Must be `http://` or `https://` with a public host; use `https://`. Literal private, loopback or link-local IPs and `localhost` are rejected, and every delivery re-checks the resolved address (see the caution below). |
| `name` | string | no | Shown in the console and the flow editor's dropdowns. Defaults to the URL. |
| `headers` | object | no | Extra request headers, name to value, for example `{"Authorization": "Bearer …"}`. At most 20. Values are stored encrypted (with `TRING_SECRETS_KEY`) and are never returned. Names Tring sets itself (`Content-Type`, `User-Agent`, `Host`, `Tring-*`, …) are refused. |
| `active` | boolean | no | Default `true`. A disabled webhook is never sent. |
| `secret` | string | no | Signing secret. When omitted, Tring generates one (`whsec_` followed by 48 hex characters). |

```bash
curl -sS -X POST "$TRING_URL/v1/webhooks" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name": "CRM", "url": "https://crm.example.com/tring/webhook", "headers": {"Authorization": "Bearer crm-token"}}'
```

**Response:** `201 Created`. This is the **only** response that includes `secret`. Store it in your secret manager now.

```json
{
  "id": "e4a1b2c3-9999-4d00-8b00-000000000001",
  "tenant_id": "7d3e9f10-0000-4000-8000-000000000001",
  "name": "CRM",
  "url": "https://crm.example.com/tring/webhook",
  "secret": "whsec_***REDACTED***",
  "header_names": ["Authorization"],
  "active": true,
  "created_at": "2026-10-24T11:20:00.120044+05:30",
  "updated_at": "2026-10-24T11:20:00.120044+05:30"
}
```

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid JSON body` |
| `400` | `url is required` |
| `400` | `url must be a public http or https URL` |
| `400` | `invalid header name "…"` / `header "…" is set by Tring` / `invalid value for header "…"` / `at most 20 headers` |
| `500` | `insert webhook failed` |

:::caution
Only register HTTPS endpoints you control. The gateway refuses to connect to loopback, private, link-local (including cloud metadata), CGNAT and other internal addresses, checked on the resolved IP of every attempt and redirect. Operators with an on-premises receiver can allow its range with `TRING_OUTBOUND_ALLOW_CIDRS`; egress filtering is still a good second layer. Treat the secret like a password: if it leaks, delete the webhook and create a new one, then repoint your flows. There's no way to rotate a secret in place.
:::

### GET /v1/webhooks

List your tenant's webhooks, newest first. Secrets and header values are never included, only header names.

```bash
curl -sS "$TRING_URL/v1/webhooks" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `200 OK`, an array of webhooks shaped like the create response, without `secret`.

**Errors**

| Status | Message |
|---|---|
| `500` | `query failed` / `scan failed` |

### PATCH /v1/webhooks/{id}

Change a webhook. Omitted fields are unchanged. The change applies to the next send, in every flow that uses the webhook.

| Field | Type | Description |
|---|---|---|
| `name` | string | New name (not empty). |
| `url` | string | New URL, checked like on create. |
| `headers` | object | Replaces **all** headers. `{}` removes them. Omit to keep the current ones. |
| `active` | boolean | `false` disables the webhook; `true` enables it. |

```bash
curl -sS -X PATCH "$TRING_URL/v1/webhooks/e4a1b2c3-9999-4d00-8b00-000000000001" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"active": false}'
```

**Response:** `200 OK`, the updated webhook (without `secret`).

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid id` / `invalid JSON body` / `name can't be empty` / a URL or header error as on create |
| `404` | `webhook not found` |
| `500` | `update webhook failed` |

### DELETE /v1/webhooks/{id}

Delete a webhook. Its delivery history is deleted with it. Flows that still name it stop sending to it; the flow editor shows those as "Deleted webhook".

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
    "event_type": "ivr.node",
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
