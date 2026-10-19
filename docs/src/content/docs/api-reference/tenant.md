---
title: Tenant & guest config
description: Read and update tenant settings, set the default guest flow, and mint single-use visitor call credentials.
---

All endpoints on this page act on the tenant the credential belongs to. They accept a tenant API key or a console session (see [Authentication](/tring/api-reference/authentication/)).

## Tenant settings

### The tenant object

| Field | Type | Description |
|---|---|---|
| `id` | UUID | Tenant ID. |
| `name` | string | Tenant name. |
| `sip_domain` | string | The tenant's SIP domain, for example `t_default.tring.local`. |
| `outbound_strategy` | string | How outbound calls pick a trunk: `failover`, `round_robin`, `manual` or `load_based`. Default `failover`. See [Numbers & trunks](/tring/features/numbers-and-trunks/). |
| `failover_causes` | string[] | FreeSWITCH hangup causes that make an outbound call try the next trunk. |
| `hold_music` | string | The `file_path` of a sound used as hold music, or `""` for the built-in default. |
| `record_calls` | boolean | Record calls once the agent and caller are connected. Default `true`. |
| `max_call_seconds` | integer | Maximum call duration, in seconds. Default `14400` (4 hours). Also sets how long the TURN credentials handed to browsers stay valid. |
| `transfer_failed_message` | string | What a caller hears when a transfer finds no one available. |
| `recording_sample_rate` | integer | Sample rate, in Hz, that call recordings are encoded at. Default `8000`. |

The default `failover_causes` are `NETWORK_OUT_OF_ORDER`, `GATEWAY_DOWN`, `USER_NOT_REGISTERED`, `SUBSCRIBER_ABSENT`, `NORMAL_TEMPORARY_FAILURE`, `RECOVERY_ON_TIMER_EXPIRE` and `NO_ROUTE_DESTINATION`.

### GET /v1/tenants/me

Read the current tenant's settings.

**Auth:** API key or console session.

```bash
curl -sS "$TRING_URL/v1/tenants/me" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response** `200 OK`

```json
{
  "id": "00000000-0000-4000-8000-000000000001",
  "name": "Default",
  "sip_domain": "t_default.tring.local",
  "outbound_strategy": "failover",
  "failover_causes": [
    "NETWORK_OUT_OF_ORDER",
    "GATEWAY_DOWN",
    "USER_NOT_REGISTERED",
    "SUBSCRIBER_ABSENT",
    "NORMAL_TEMPORARY_FAILURE",
    "RECOVERY_ON_TIMER_EXPIRE",
    "NO_ROUTE_DESTINATION"
  ],
  "hold_music": "",
  "record_calls": true,
  "max_call_seconds": 14400,
  "transfer_failed_message": "Sorry, no one is available to take your call right now. Please try again later.",
  "recording_sample_rate": 8000
}
```

**Errors:** only the authentication errors (`401`).

### PATCH /v1/tenants/me

Update one or more tenant settings. Fields you leave out keep their current value.

**Auth:** API key or console session.

**Request body** (at least one field is required)

| Field | Type | Required | Description |
|---|---|---|---|
| `outbound_strategy` | string | no | One of `failover`, `round_robin`, `manual`, `load_based`. |
| `failover_causes` | string[] | no | Replaces the whole list of hangup causes that trigger failover to the next trunk. |
| `hold_music` | string | no | A sound's `file_path` (from [`GET /v1/audio`](/tring/api-reference/audio/#get-v1audio)) to use as hold music, or `""` to go back to the built-in default. The path isn't checked against your sound library, so copy it exactly. |
| `record_calls` | boolean | no | Turn call recording on or off. |
| `max_call_seconds` | integer | no | Maximum call duration in seconds, from `60` to `86400` (1 minute to 24 hours). |
| `transfer_failed_message` | string | no | Text played when a transfer finds no one available. It's turned into speech when you save it. Sending `""` makes that moment silent. |
| `recording_sample_rate` | integer | no | Recording sample rate in Hz: `8000`, `12000`, `16000`, `24000` or `48000`. Applies to calls that start after the change. |

```bash
curl -sS -X PATCH "$TRING_URL/v1/tenants/me" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "outbound_strategy": "round_robin",
    "record_calls": false,
    "max_call_seconds": 3600,
    "hold_music": "/var/lib/tring/sounds/00000000-0000-4000-8000-000000000001/7b0e9a52-0000-4000-8000-0000000000b1.ogg"
  }'
```

**Response** `200 OK` with the full, updated [tenant object](#the-tenant-object).

**Errors**

| Status | `error` |
|---|---|
| `400` | `invalid JSON body` |
| `400` | `at least one of outbound_strategy, failover_causes, hold_music, record_calls, max_call_seconds, transfer_failed_message or recording_sample_rate is required` |
| `400` | `recording_sample_rate must be one of 8000, 12000, 16000, 24000, 48000` |
| `400` | `max_call_seconds must be between 60 and 86400 (1 min–24 h)` |
| `400` | `outbound_strategy must be failover\|round_robin\|manual\|load_based` |
| `500` | `update failed: …` |

:::note
`transfer_failed_message` is converted to audio with the server's text-to-speech engine when you save it. If text-to-speech isn't set up, or the conversion fails, the update still succeeds, but the caller hears silence and then the call ends. Check the API logs for a `tts:` warning if the message doesn't play.
:::

## Guest (visitor) calls

Website visitors call you through the [visitor widget](/tring/sdk/visitor-widget/). Each visitor call lands in an IVR flow. You can name the flow when you mint credentials, or fall back to the tenant's default guest flow.

### GET /v1/guest-config

Read the tenant's default guest flow.

**Auth:** API key or console session.

```bash
curl -sS "$TRING_URL/v1/guest-config" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response** `200 OK`

```json
{
  "default_flow_id": "4c8b1f20-0000-4000-8000-0000000000f1"
}
```

`default_flow_id` is `null` when no default is set.

**Errors**

| Status | `error` |
|---|---|
| `500` | `query failed` |

### PATCH /v1/guest-config

Set or clear the default guest flow.

**Auth:** API key or console session.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `default_flow_id` | string | yes | A flow UUID to use as the default, or `""` to clear it. The flow must belong to your tenant and be active. |

```bash
curl -sS -X PATCH "$TRING_URL/v1/guest-config" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"default_flow_id":"4c8b1f20-0000-4000-8000-0000000000f1"}'
```

**Response** `200 OK`, the same shape as [`GET /v1/guest-config`](#get-v1guest-config).

**Errors**

| Status | `error` |
|---|---|
| `400` | `invalid JSON body` |
| `400` | `default_flow_id required` (field missing or `null`) |
| `400` | `invalid default_flow_id` (not a UUID) |
| `400` | `flow not found or inactive` |
| `400` | `flow lookup failed` |
| `500` | `query failed`, `update failed` |

### POST /v1/guest-credentials

Mint the credentials a visitor's browser needs to place **one** web call. Call this from your backend with your API key, then return the response to the visitor's browser, which passes it to the visitor widget.

**Auth:** API key or console session. Never call this from the browser: it needs your API key.

**Request body** (optional, an empty body is fine)

| Field | Type | Required | Description |
|---|---|---|---|
| `flow_id` | string | no | The IVR flow this call runs. Must be an active flow in your tenant. Defaults to the tenant's `default_flow_id`. |
| `client_ref` | string | no | Your identifier for the caller, for example a CRM customer ID. It's recorded on the call so you can match the call log to the customer. Only pass a value you've verified, such as the logged-in user's ID from your own session. Leave it out for anonymous visitors. |

```bash
curl -sS -X POST "$TRING_URL/v1/guest-credentials" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"flow_id":"4c8b1f20-0000-4000-8000-0000000000f1","client_ref":"customer-123"}'
```

**Response** `200 OK`

```json
{
  "gateway_url": "https://gw.example.com",
  "flow_id": "4c8b1f20-0000-4000-8000-0000000000f1",
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.example.signature",
  "ice_servers": [
    { "urls": "stun:stun.example.com:3478" },
    {
      "urls": "turn:turn.example.com:3478",
      "username": "1760700000:customer-123",
      "credential": "example-credential"
    }
  ]
}
```

| Field | Description |
|---|---|
| `gateway_url` | The media gateway URL the browser connects to (`TRING_GATEWAY_PUBLIC_URL`). |
| `flow_id` | The flow the call is pinned to. |
| `token` | The visitor call token. Valid for **2 minutes**, and only for **one** call. |
| `ice_servers` | STUN/TURN servers in the browser's `RTCIceServer` format. TURN entries appear only if TURN is configured. With a TURN secret, each response gets its own short-lived TURN credential. |

**How the visitor flow works**

1. The visitor clicks "Call us" on your site.
2. Your frontend asks **your** backend for credentials.
3. Your backend checks the request (signed-in session, CAPTCHA or rate limit for anonymous visitors), then calls `POST /v1/guest-credentials`.
4. Your backend returns the response to the browser, and the visitor widget connects straight to the media gateway with it.
5. The gateway accepts the token once. A second call needs new credentials.

The flow is fixed in the token, so a visitor can't change which flow they reach.

**Errors**

| Status | `error` |
|---|---|
| `400` | `invalid flow_id` (not a UUID) |
| `400` | `flow not found or inactive` |
| `400` | `flow lookup failed` |
| `400` | `no flow for this guest call — pass flow_id, or set a default flow in Settings` |
| `500` | `tenant config lookup failed` |
| `500` | `token issue failed` |

:::caution
Anyone who can reach your credentials endpoint can make calls into your IVR. Rate-limit it, and put a CAPTCHA in front of it if anonymous visitors can call.
:::
