---
title: Calls
description: Place outbound calls, bridge two phone numbers, list and fetch call records, read a call's event timeline and recording, and hold, resume or supervise live calls.
---

Every call Tring handles gets a row in the call log: inbound phone calls, web calls, and outbound calls your backend places. This page covers placing outbound calls, reading the log, and controlling live calls. For the concepts, see [Outbound calls & masking](/tring/features/outbound-calls/), [Call control](/tring/features/call-control/) and [Recording & call logs](/tring/features/recording-and-call-logs/).

Unless a section says otherwise, routes take a tenant API key or a console session. See [Authentication](/tring/api-reference/authentication/).

## The call object

| Field | Type | Description |
|---|---|---|
| `id` | UUID | Call ID. |
| `tenant_id` | UUID | Owning tenant. |
| `direction` | string | `inbound` or `outbound`. |
| `status` | string | See [Status values](#status-values). |
| `from_number` | string | Calling party. For outbound calls, the caller ID presented. For web calls, the visitor reference. |
| `to_number` | string | Called party. For web calls, the flow's name. |
| `agent_id` | UUID | The agent who handled the call. Omitted when none. |
| `agent_name` | string | That agent's display name, or `sip_username` if it has none. Omitted when none. |
| `client_ref` | string | The trusted caller reference your backend attached when minting the visitor's call token. Omitted when none. |
| `source` | string | Channel: `web`, `phone` or `whatsapp`. |
| `leg_id` | string | Opaque per-call leg identifier. Treat it as an opaque string. |
| `started_at` | timestamp | |
| `answered_at` | timestamp | Omitted until answered. |
| `ended_at` | timestamp | Omitted until ended. |
| `duration_seconds` | integer | Omitted until ended. |
| `hangup_cause` | string | Who ended the call. `client` (web caller), `agent`, `caller_hangup` (phone caller), `callee_hangup` (the dialed party on an outbound or IVR-dialed call) or `system` (flow end, `hangup` node, no answer). Omitted until ended. |
| `recording_url` | string | API path of the recording, `/v1/calls/{id}/recording`. Omitted when the call wasn't recorded. Fetch it with your credential; it isn't a public link. |
| `ivr_path` | array | The IVR steps the call took, same shape as the [simulator's trace steps](/tring/api-reference/flows/#trace-steps), plus an `at` timestamp per step on live calls. `[]` for calls that didn't run a flow. |
| `room_id` | string | Media-gateway room, used to match live dashboard events. Omitted for calls that don't have one. |

### Status values

| Status | Meaning |
|---|---|
| `ringing` | The call has started and nobody has answered yet. Includes time in the IVR and in the queue. |
| `answered` | An agent (or the called party) is connected. |
| `ended` | The call is over. |
| `failed` | The call failed before connecting. Defined in the schema; not written by the current code paths. |

## Placing calls

:::danger
Every outbound call is dialed on your trunk and billed to you. Never dial a number an end user supplied (a form field, a CRM note, an IVR `gather`) without validating it and checking it against an allow-list in your backend. With a trunk whose `provider_type` is `generic`, Tring does no destination validation at all. See [Trunks](/tring/api-reference/trunks/#provider_type).
:::

### POST /v1/calls

Click-to-call for an agent. Tring rings the agent's own leg first, auto-answering it in the agent widget (the agent clicked to call), then dials `to` over your outbound trunk and bridges the two.

Your backend chooses the agent and the destination. The agent's browser never receives the dialed number; it only sees `agent_label`.

**Auth:** tenant API key or console session.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `agent_id` | UUID | yes | The agent to connect. Must be the agent's UUID (not its `sip_username`) and belong to your tenant. |
| `to` | string | yes | Destination number. Normalised by the first-choice trunk's `provider_type` before dialing. |
| `agent_label` | string | no | Text shown to the agent for this call, for example a ticket reference. Don't put the customer's number or other personal data here. Empty shows a generic incoming-call label. |
| `caller_id` | string | no | The number the callee sees. Must be one of your tenant's **active** numbers (any type), matched exactly. Use it, for example, to present your support line on a masked callback. Empty uses each trunk's own outbound DID. |

What happens:

1. `caller_id`, if set, is checked against your tenant's active numbers.
2. Trunks are chosen from your active outbound numbers by the tenant's `outbound_strategy`: `failover` tries every route in priority order, `round_robin` picks the next one, `manual` uses only the lowest-priority one. `load_based` is reserved and returns `400`.
3. `to` is normalised. Each trunk leg presents that trunk's own DID as caller ID, or `caller_id` on every leg when you set it. With `failover` and more than one route, FreeSWITCH moves to the next trunk only on the tenant's configured failover causes.
4. The call is created with `status: "ringing"` and a [`call.ringing` webhook](/tring/api-reference/webhooks/#callringing) is sent.

:::caution
With no active outbound number on an active trunk, Tring falls back to a FreeSWITCH loopback dial meant for local development, with no normalisation. Make sure production tenants have at least one outbound number with a trunk.
:::

```bash
curl -sS -X POST "$TRING_URL/v1/calls" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "agent_id": "a1b2c3d4-0000-4000-8000-000000000001",
    "to": "+919800000003",
    "agent_label": "Ticket #4821",
    "caller_id": "+919800000001"
  }'
```

**Response:** `201 Created` with the new call.

```json
{
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
```

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid JSON body` (including an `agent_id` that isn't a UUID) |
| `400` | `agent_id and to are required` |
| `400` | `caller_id "…" is not one of this tenant's numbers` |
| `400` | `invalid destination "…": …`, for example `not a valid Indian mobile number` |
| `400` | `load_based strategy is reserved and not yet implemented` |
| `404` | `agent not found` |
| `500` | `agent lookup failed` / `insert call: …` |
| `502` | `originate failed: …` (FreeSWITCH rejected or couldn't be reached) |

:::caution
Calls placed with `POST /v1/calls` and `POST /v1/calls/connect` go out through FreeSWITCH directly. In the current code, nothing moves these rows past `ringing` when the call is answered or ends. Don't rely on their `status`, `answered_at` or `duration_seconds`. Calls that go through the media gateway (inbound phone, web, and agent-dialed calls from the widget) are updated normally.
:::

### POST /v1/calls/connect

Bridge two phone numbers over your trunk, without a softphone. Tring rings `agent_number` first; when it answers, it dials `client_number` and connects them. Both legs present the first-choice trunk's own DID as caller ID. This is the callback pattern: both parties just need a phone.

**Auth:** tenant API key or console session.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `agent_number` | string | yes | Rung first. |
| `client_number` | string | yes | Dialed once the agent answers. |

Both numbers are normalised by the first-choice trunk's `provider_type`. Only the first-choice trunk is used; there's no failover between trunks. There's no `caller_id` override.

```bash
curl -sS -X POST "$TRING_URL/v1/calls/connect" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"agent_number": "+919800000004", "client_number": "+919800000005"}'
```

**Response:** `201 Created`. `from_number` is the agent's number, `to_number` the client's, and there's no `agent_id`. A [`call.ringing` webhook](/tring/api-reference/webhooks/#callringing) is sent.

```json
{
  "id": "c0ffee00-6666-4a00-8f00-000000000002",
  "tenant_id": "7d3e9f10-0000-4000-8000-000000000001",
  "direction": "outbound",
  "status": "ringing",
  "from_number": "+919800000004",
  "to_number": "+919800000005",
  "source": "phone",
  "leg_id": "6b8d2f10-7777-4c00-8a00-000000000002",
  "started_at": "2026-10-16T11:05:10.660031+05:30"
}
```

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid JSON body` |
| `400` | `agent_number and client_number are required` |
| `400` | `invalid agent_number "…": …` / `invalid client_number "…": …` |
| `400` | `load_based strategy is reserved and not yet implemented` |
| `500` | `insert call: …` |
| `502` | `originate failed: …` |

## Reading the call log

### GET /v1/calls

List calls, newest first.

**Auth:** tenant API key or console session.

**Query parameters**

| Param | Description |
|---|---|
| `status` | Only calls with this status: `ringing`, `answered`, `ended` or `failed`. |
| `q` | Search. Digits (with any `+`, spaces, dashes or brackets) match the **caller or called number** in any format: `9800000001`, `+91 98000-00001` and `09800000001` all find a call stored as `+919800000001`. Anything else is a **call ID** (or FreeSWITCH leg ID), matched from the start, so a shortened ID works. |
| `active` | `true` returns only in-progress calls (`ringing` or `answered`, not ended), **oldest first**, up to 100, unpaginated. `page`, `limit` and `status` are ignored. |
| `page` | 1-based page number. See [Pagination](/tring/api-reference/overview/#pagination). |
| `limit` | With `page`: page size, default `25`. Without `page`: how many of the newest calls to return, default `50`. Max `200` either way. |

```bash
curl -sS "$TRING_URL/v1/calls?status=ended&page=1&limit=25" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `200 OK` with the pagination envelope.

```json
{
  "items": [
    {
      "id": "c0ffee00-6666-4a00-8f00-000000000003",
      "tenant_id": "7d3e9f10-0000-4000-8000-000000000001",
      "direction": "inbound",
      "status": "ended",
      "from_number": "+919800000006",
      "to_number": "+919800000001",
      "agent_id": "a1b2c3d4-0000-4000-8000-000000000001",
      "agent_name": "Agent One",
      "source": "phone",
      "leg_id": "7d3e9f10-0000-4000-8000-000000000001/pstn-example-call-1",
      "started_at": "2026-10-16T10:30:00.000000+05:30",
      "answered_at": "2026-10-16T10:30:41.250000+05:30",
      "ended_at": "2026-10-16T10:34:12.900000+05:30",
      "duration_seconds": 252,
      "hangup_cause": "caller_hangup",
      "recording_url": "/v1/calls/c0ffee00-6666-4a00-8f00-000000000003/recording",
      "ivr_path": [
        { "node_id": "n_start", "node_type": "start", "action": "passthrough", "outcome": "default",
          "at": "2026-10-16T10:30:01.020000+05:30" },
        { "node_id": "n_menu", "node_type": "menu", "action": "prompt_dtmf", "dtmf_input": "1",
          "outcome": "digit:1", "at": "2026-10-16T10:30:01.030000+05:30" },
        { "node_id": "n_sales", "node_type": "transfer", "action": "transfer", "detail": "sales",
          "outcome": "completed", "at": "2026-10-16T10:30:09.400000+05:30" }
      ],
      "room_id": "7d3e9f10-0000-4000-8000-000000000001/pstn-example-call-1"
    }
  ],
  "total": 1342,
  "limit": 25,
  "offset": 0
}
```

**Errors**

| Status | Message |
|---|---|
| `500` | `count failed` / `query failed` |

An unknown `status` value returns an empty list, not an error.
| `500` | `query failed` / `query failed: …` / `scan failed` |

### GET /v1/calls/{id}

Fetch one call.

**Auth:** tenant API key or console session.

```bash
curl -sS "$TRING_URL/v1/calls/c0ffee00-6666-4a00-8f00-000000000003" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `200 OK` with the call object.

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid id` |
| `404` | `call not found` |
| `500` | `query failed` |

### GET /v1/calls/{id}/events

The call's timeline: what happened and when, in order. It covers routing (who was rung, who didn't answer), answers, transfers and supervision, which the call row alone can't show. Written by the media gateway as things happen.

**Auth:** tenant API key or console session.

```bash
curl -sS "$TRING_URL/v1/calls/c0ffee00-6666-4a00-8f00-000000000003/events" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `200 OK` with an array of events, oldest first. `[]` for a call with no recorded events (older calls, or calls placed through FreeSWITCH directly). An unknown call ID also returns `[]`, not `404`.

| Field | Type | Description |
|---|---|---|
| `type` | string | See below. |
| `at` | timestamp | When it happened. |
| `detail` | object | Type-specific. |

| `type` | `detail` fields |
|---|---|
| `started` | `from`, `to`, `source`, `direction` |
| `ring` | `agent` (display name), `sip`, `agent_id` (when the SIP matched an agent) |
| `no_answer` | `agents`: names of everyone rung in that attempt |
| `answered` | `agent` (may be `""`), `agent_id` (may be `null`) |
| `transfer` | `from`, `to`: agent names, or a team name |
| `supervise` | `action` (`start`, `mode` or `end`), `supervisor`, and `mode` on `start` and `mode` |
| `ended` | `cause`, `duration_seconds` |

```json
[
  { "type": "started", "at": "2026-10-16T10:30:00.010000+05:30",
    "detail": { "from": "+919800000006", "to": "+919800000001", "source": "phone", "direction": "inbound" } },
  { "type": "ring", "at": "2026-10-16T10:30:09.410000+05:30",
    "detail": { "agent": "Agent One", "sip": "agent-one", "agent_id": "a1b2c3d4-0000-4000-8000-000000000001" } },
  { "type": "answered", "at": "2026-10-16T10:30:41.250000+05:30",
    "detail": { "agent": "Agent One", "agent_id": "a1b2c3d4-0000-4000-8000-000000000001" } },
  { "type": "ended", "at": "2026-10-16T10:34:12.900000+05:30",
    "detail": { "cause": "caller_hangup", "duration_seconds": 252 } }
]
```

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid id` |
| `500` | `query failed` / `scan failed` |

### GET /v1/calls/{id}/recording

Download a call's recording. Recording starts when the agent answers, so the IVR and queue time isn't included. Tenants turn recording on with `record_calls` in [tenant settings](/tring/api-reference/tenant/).

**Auth:** tenant API key or console session. The recording is streamed through the API with your credential; there's no public or pre-signed link.

```bash
curl -sS "$TRING_URL/v1/calls/c0ffee00-6666-4a00-8f00-000000000003/recording" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -o call-recording.ogg
```

**Response:** `200 OK` with the audio file. `Content-Type` follows the stored format: `audio/ogg` (`.ogg`/`.opus`), `audio/mpeg` (`.mp3`) or `audio/wav`. The file comes from S3 when the recording has been uploaded there, otherwise from the local recordings directory.

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid id` |
| `404` | `call not found` |
| `404` | `recording not available` (not recorded, still being finalised, or the file is missing) |
| `500` | `query failed` |

:::caution
Recordings are personal data. Fetch them server-side, store them only where your retention policy allows, and don't hand an API key to a browser to play them.
:::

## Controlling live calls

### POST /v1/calls/{id}/hold

Put the customer on hold: they hear the tenant's hold music (or the built-in default), looped, in place of the agent. Works through FreeSWITCH, by injecting audio into the customer's leg.

Agents holding their own call from the widget use the agent-token route `POST /v1/sdk/calls/hold` instead.

**Auth:** tenant API key or console session.

**Request body:** none.

```bash
curl -sS -X POST "$TRING_URL/v1/calls/c0ffee00-6666-4a00-8f00-000000000003/hold" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `200 OK`

```json
{ "id": "c0ffee00-6666-4a00-8f00-000000000003", "held": true }
```

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid id` |
| `404` | `call not found` |
| `409` | `call is not active` (status isn't `answered`) |
| `409` | `call has no media leg yet` |
| `500` | `query failed` |
| `502` | `hold command failed` |

### POST /v1/calls/{id}/resume

Take the call off hold. Same rules and errors as hold.

**Auth:** tenant API key or console session.

```bash
curl -sS -X POST "$TRING_URL/v1/calls/c0ffee00-6666-4a00-8f00-000000000003/resume" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `200 OK`

```json
{ "id": "c0ffee00-6666-4a00-8f00-000000000003", "held": false }
```

### POST /v1/calls/{id}/supervise

Get a short-lived token for a supervisor to join a live call through the media gateway.

**Auth:** **console session only**. API keys are rejected, because the permission is per user. The signed-in user must have the supervise permission.

Any call that is `ringing` or `answered` and has a media room can be supervised: web, phone and WhatsApp calls. Calls placed with `POST /v1/calls` have no media room.

**Request body:** none.

```bash
curl -sS -X POST "$TRING_URL/v1/calls/c0ffee00-6666-4a00-8f00-000000000004/supervise" \
  -H "Authorization: Bearer $TRING_CONSOLE_TOKEN"
```

**Response:** `200 OK`

```json
{
  "gateway_url": "https://gw.example.com",
  "room": "web-example-room-1",
  "token": "eyJhbGciOi...REDACTED",
  "ice_servers": [
    { "urls": "stun:stun.example.com:3478" }
  ]
}
```

| Field | Description |
|---|---|
| `gateway_url` | Browser-reachable media gateway base URL. |
| `room` | The room the token is pinned to. |
| `token` | Supervisor token, valid for **2 minutes**: long enough to open the gateway connection. |
| `ice_servers` | STUN/TURN servers for the browser, in `RTCIceServer` shape. |

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid id` |
| `401` | `console session required` / `invalid console session` / `bad user id` |
| `403` | `not permitted to supervise` |
| `404` | `call not found` |
| `409` | `call is not live` |
| `409` | `call has no media room` |
| `500` | `permission lookup failed` / `call lookup failed` / `token issue failed` |
