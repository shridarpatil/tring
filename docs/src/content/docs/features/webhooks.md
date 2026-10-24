---
title: Webhooks
description: Register webhooks, pick them per IVR node and flow, payloads, HMAC-SHA256 signature verification in Go and Node, retries and the delivery log.
---

Webhooks push call events from Tring to your backend as signed HTTP `POST` requests. You register each webhook once, a URL with optional headers and an on/off status, in the console (**Webhooks**) or with the [Webhooks API](/tring/api-reference/webhooks/). Then you choose, in each IVR flow, which steps send to which webhook. Different flows (for example one per client) can send to different endpoints.

## Choosing what sends

| Where | Event | Sent when |
|---|---|---|
| Any IVR node: **Notify webhook** in the node's properties panel | `ivr.node` | The step finishes, with its result and the caller's input. A **Transfer** sends it when someone answers, or when nobody does. |
| The flow: **On call end** in the flow editor's toolbar | `call.ended` | A call through this flow ends, however it ended: the caller hung up in a menu or the queue, after talking to an agent, or the flow ended the call. |

Nodes that send a webhook show a bell icon on the canvas.

- **A gather node** sends the digits the caller entered, for example an account number to look up in your CRM.
- **A menu node** sends the option chosen (`digit:2`).
- **A transfer node** sends `answered` the moment an agent or the dialed number picks up, useful for a CRM screen-pop, or `no_answer`.
- **On call end** sends the settled call record: duration, hang-up cause and the agent who handled it.

A webhook that is **disabled** on the Webhooks page is never sent, from any flow. Turn it back on and every flow that picks it sends again, with no flow changes. Webhooks are sent in the background, so a slow or failing endpoint never delays the caller. This is the difference from the [HTTP callback node](/tring/features/ivr-flows/#http-callback), which waits for your response and branches on it.

:::note
- `call.ended` arrives before the recording is merged, so it has no `recording_url`. Fetch the call or its recording later.
- Calls your backend places with `POST /v1/calls` or `POST /v1/calls/connect` don't run an IVR flow and send no webhooks.
- Agent availability changes aren't webhooks. Watch them on the dashboard event stream.
:::

## Registering a webhook

```bash
curl -sS -X POST https://api.example.com/v1/webhooks \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"CRM","url":"https://crm.example.com/hooks/tring","headers":{"Authorization":"Bearer crm-token"}}'
```

- **Headers** are sent with every request, for example an auth token your endpoint checks. Their values are stored encrypted and never shown again, only their names. Editing a webhook's headers replaces the whole set.
- **Secret:** if you don't pass one, Tring generates one (`whsec_…`). It's returned **only in this response**, so store it in your secret manager. It signs every request (see [Signature](#signature)).

Changes to a webhook (URL, headers, on/off) apply to the next send. The gateway caches each tenant's webhooks in Redis and the API clears that cache on every change.

## Payload

```http
POST /hooks/tring HTTP/1.1
Content-Type: application/json
User-Agent: Tring-Webhook/0.1
Authorization: Bearer crm-token
Tring-Event: ivr.node
Tring-Delivery: 6f1c2d3e-0000-4000-8000-000000000001
Tring-Signature: t=1760000000,v1=5d41402abc4b2a76b9719d911017c592...
```

```json
{
  "event": "ivr.node",
  "delivery_id": "6f1c2d3e-0000-4000-8000-000000000001",
  "created": "2026-10-24T09:30:00Z",
  "data": {
    "call_id": "…",
    "flow_id": "…",
    "node_id": "n_account",
    "node_type": "gather",
    "node_label": "Account number",
    "outcome": "default",
    "dtmf": "40213",
    "vars": { "caller": "+919800000001", "did": "+912265915000", "account": "40213" }
  }
}
```

`call.ended` carries `{ "call": <call record> }`, the same shape as the [Calls API](/tring/api-reference/calls/), so one decoder works for both. Both payloads are described field by field in the [Webhooks API](/tring/api-reference/webhooks/#ivrnode). `delivery_id` matches the `Tring-Delivery` header; use it to drop duplicates, because a retry resends the same delivery.

## Signature

The `Tring-Signature` header is `t=<unix seconds>,v1=<hex>`, where:

```
v1 = hex( HMAC-SHA256( secret, "<t>.<raw request body>" ) )
```

To verify:

1. Read the **raw** body bytes. Do not parse and re-serialize the JSON first.
2. Split the header into `t` and `v1`.
3. Compute the HMAC over `t + "." + body` with your secret and compare it to `v1` in **constant time**.
4. Reject requests where `t` is too old (for example more than 5 minutes), to block replays.

### Go

```go
package hooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const tolerance = 5 * time.Minute

func verify(secret, header string, body []byte, now time.Time) bool {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || sig == "" {
		return false
	}
	if d := now.Sub(time.Unix(t, 0)); d > tolerance || d < -tolerance {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(sig))
}

func Handler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	secret := os.Getenv("TRING_WEBHOOK_SECRET")
	if !verify(secret, r.Header.Get("Tring-Signature"), body, time.Now()) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	// Decode body, dedupe on delivery_id, then hand off to a queue.
	w.WriteHeader(http.StatusNoContent)
}
```

### Node.js

Uses only the built-in `crypto` module, no extra packages.

```js
import crypto from "node:crypto";
import http from "node:http";

const SECRET = process.env.TRING_WEBHOOK_SECRET;
const TOLERANCE_S = 300;

function verify(header, rawBody) {
  const parts = Object.fromEntries(
    (header || "").split(",").map((p) => p.split("=", 2)),
  );
  const t = Number(parts.t);
  if (!Number.isInteger(t) || !parts.v1) return false;
  if (Math.abs(Date.now() / 1000 - t) > TOLERANCE_S) return false;

  const expected = crypto
    .createHmac("sha256", SECRET)
    .update(`${parts.t}.`)
    .update(rawBody)
    .digest("hex");
  const a = Buffer.from(expected, "hex");
  const b = Buffer.from(parts.v1, "hex");
  return a.length === b.length && crypto.timingSafeEqual(a, b);
}

http
  .createServer((req, res) => {
    const chunks = [];
    req.on("data", (c) => chunks.push(c));
    req.on("end", () => {
      const raw = Buffer.concat(chunks);
      if (!verify(req.headers["tring-signature"], raw)) {
        res.writeHead(401).end();
        return;
      }
      const event = JSON.parse(raw.toString("utf8"));
      // Dedupe on event.delivery_id, then process asynchronously.
      res.writeHead(204).end();
    });
  })
  .listen(3000);
```

If you use a framework, make sure the route gets the raw body (for example `express.raw({ type: "application/json" })`), not a parsed object.

## Delivery and retries

- Tring records each delivery as `pending`, then sends it in the background, so your endpoint's speed never slows a call.
- The URL must be `http://` or `https://` and resolve to a public address. Loopback, private, link-local (including cloud metadata) and other internal addresses are refused when the webhook is created (for literal IPs and `localhost`) and again on every delivery attempt, after DNS resolution and on redirects (at most 3 are followed). A refused attempt fails like a connection error. An on-premises receiver needs its range in [`TRING_OUTBOUND_ALLOW_CIDRS`](/tring/getting-started/configuration/).
- Each attempt has a 10-second timeout. Any `2xx` response is success; anything else (including a timeout or connection error) is a failure.
- A failed delivery is retried up to **5 more times**, waiting 1, 2, 4, 8 and 16 seconds between attempts (6 attempts, about 30 seconds in total).
- After the last failure the delivery is marked `exhausted`. There is no automatic redelivery later and no replay endpoint yet.

Respond with `2xx` quickly and do the work asynchronously. Make your handler idempotent.

## Delivery log

`GET /v1/webhooks/{id}/deliveries` returns the latest 100 deliveries for a webhook, newest first:

| Field | What it is |
|---|---|
| `id` | Delivery id (the `Tring-Delivery` header). |
| `event_type` | The event. |
| `status` | `pending`, `success` or `exhausted`. |
| `attempts` | Attempts made so far. |
| `response_status` | Last HTTP status from your endpoint. |
| `last_error` | Last error, for example `non-2xx: 500`. |
| `delivered_at`, `created_at` | Timestamps. |

The console shows the same log under **Webhooks**.

## Security checklist

- Use an `https://` endpoint.
- Verify every signature and enforce the timestamp tolerance.
- Keep the webhook secret in a secret manager, never in front-end code or source control. To rotate it, register a new webhook with a new secret, switch your receiver to accept both, repoint your flows to the new webhook, then delete the old one.
- Treat payloads as data: validate fields before using them.
