---
title: Queues & stats
description: Live queue depth per team, today's call statistics, and the live dashboard event stream (SSE).
---

These endpoints power the console's live ops dashboard. You can use them to build your own wallboard. All accept a tenant API key or a console session.

### GET /v1/queues

How many callers are waiting in each team's queue right now, and how many calls are connected to an agent.

**Auth:** API key or console session.

```bash
curl -sS "$TRING_URL/v1/queues" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response** `200 OK`

```json
{
  "total": 3,
  "on_call": 5,
  "queues": [
    { "team": "billing", "waiting": 0 },
    { "team": "support", "waiting": 3 }
  ]
}
```

| Field | Type | Description |
|---|---|---|
| `total` | integer | Callers waiting across all teams. |
| `on_call` | integer | Answered calls currently connected to an agent. |
| `queues` | array | One entry per team, including teams with nobody waiting. |
| `queues[].team` | string | Team name. |
| `queues[].waiting` | integer | Callers waiting in that team's queue, phone and web combined. |

Queue depth is read from Redis. Without Redis, every team reports `0`.

**Errors**

| Status | `error` |
|---|---|
| `500` | `query failed` |

### GET /v1/stats/overview

Call statistics for today so far, plus calls per hour for the last 12 hours.

**Auth:** API key or console session.

```bash
curl -sS "$TRING_URL/v1/stats/overview" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response** `200 OK`

```json
{
  "calls_today": 128,
  "answered_pct": 87,
  "abandoned_today": 9,
  "failed_today": 2,
  "avg_wait_seconds": 14,
  "avg_talk_seconds": 212,
  "hourly": [
    { "hour": "2026-10-15T23:00:00+05:30", "count": 0 },
    { "hour": "2026-10-16T00:00:00+05:30", "count": 1 },
    { "hour": "2026-10-16T10:00:00+05:30", "count": 17 }
  ]
}
```

(The `hourly` array above is shortened. A real response always has 12 entries.)

| Field | Type | Description |
|---|---|---|
| `calls_today` | integer | Calls started since midnight. |
| `answered_pct` | integer | Percentage of today's calls that were answered, from `0` to `100`, rounded. `0` when there are no calls. |
| `abandoned_today` | integer | Callers who were transferred to a team or agent but hung up before anyone answered. |
| `failed_today` | integer | Calls with status `failed` (they errored before connecting). |
| `avg_wait_seconds` | integer | Average time from call start to answer, over answered calls. This includes IVR time, not just queue time. |
| `avg_talk_seconds` | integer | Average duration of today's calls that have a recorded duration. |
| `hourly` | array | Always 12 entries, oldest first, ending with the current hour. Hours with no calls have `count: 0`. |
| `hourly[].hour` | timestamp | Start of the hour. |
| `hourly[].count` | integer | Calls started in that hour. |

"Today" and the hour boundaries use the database server's time zone, so set it to your business time zone (for example `Asia/Kolkata`) when you deploy.

**Errors**

| Status | `error` |
|---|---|
| `500` | `query failed` |

### GET /v1/stream/dashboard

A live feed of dashboard events as [Server-Sent Events](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events). The first event is a full snapshot. After that you get call, agent, queue and stats updates as they happen.

**Auth:** API key or console session, in the `Authorization` header.

:::note
The browser's built-in `EventSource` can't send an `Authorization` header. Read the stream with `fetch()` and a stream reader instead (the console does this), or consume it from your backend. Never put an API key in a URL or in browser code.
:::

```bash
curl -sS -N "$TRING_URL/v1/stream/dashboard" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

The response is `Content-Type: text/event-stream`. Each event is an `event:` line naming the type and a `data:` line of JSON, followed by a blank line:

```
event: snapshot
data: {"calls":[...],"agents":[...],"queues":{...},"stats":{...}}

event: call.ringing
data: {...}

: ping
```

The stream has no 30-second request timeout and stays open until you close it. A `: ping` comment is sent every 20 seconds so proxies don't close the idle connection. The API sets `X-Accel-Buffering: no` to turn off nginx response buffering. Behind any other proxy, turn off response buffering yourself.

**Event types**

| Event | When | `data` |
|---|---|---|
| `snapshot` | First event on every connection | The full dashboard state. See below. |
| `call.ringing` | A call starts ringing | Call event from the media gateway |
| `call.routing` | A call moves between IVR and ringing agents | Call event from the media gateway |
| `call.answered` | A call is answered | Call event from the media gateway |
| `call.ended` | A call ends | Call event from the media gateway |
| `call.transferred` | A call is transferred | Call event from the media gateway |
| `call.supervised` | A supervisor joins or leaves a call | `{"room", "mode", "present", "supervisor"}`. `present` is `false` when the supervisor left. |
| `agent.presence` | An agent's connection state changes (connects, goes on or off a call, disconnects) | `{"sip_username", "status"}`, where `status` is `online`, `on_call` or `offline`. This is the raw connection state: combine it with `available_for_calls` to get `away`. |
| `agent.status` | An agent's availability changes ([`POST /v1/agents/{id}/availability`](/tring/api-reference/agents/#post-v1agentsidavailability)) | `{"agent_id", "sip_username", "available_for_calls"}` |
| `queues` | Queue depth or the on-call count may have changed | Same shape as [`GET /v1/queues`](#get-v1queues) |
| `stats` | Today's stats may have changed | Same shape as [`GET /v1/stats/overview`](#get-v1statsoverview) |

`queues` and `stats` are recalculated shortly after the call and queue events that affect them, with bursts combined into one update. They are not sent on a timer.

**Snapshot**

```json
{
  "calls": [],
  "agents": [],
  "queues": { "total": 0, "on_call": 0, "queues": [] },
  "stats": {
    "calls_today": 0,
    "answered_pct": 0,
    "abandoned_today": 0,
    "failed_today": 0,
    "avg_wait_seconds": 0,
    "avg_talk_seconds": 0,
    "hourly": []
  }
}
```

| Field | Description |
|---|---|
| `calls` | Calls currently in progress, the same objects as [`GET /v1/calls?active=true`](/tring/api-reference/calls/). |
| `agents` | All agents with live `status`, the same as [`GET /v1/agents`](/tring/api-reference/agents/#get-v1agents). |
| `queues` | Same as `GET /v1/queues`. |
| `stats` | Same as `GET /v1/stats/overview`. |

If one part of the snapshot fails to load, it is sent empty rather than failing the stream.

**Reconnecting**

Events are not replayed. If your connection drops, or the server drops a client that falls too far behind, reconnect. You get a fresh `snapshot`, which brings you back to the current state. Back off between attempts (the console starts at 1 second and doubles to a maximum of 15 seconds). A `401` means the credential is invalid: don't retry with it.

**Errors**

| Status | `error` |
|---|---|
| `401` | Authentication errors, before the stream starts |
| `500` | `streaming unsupported` |

Without Redis, the stream still sends the snapshot and the keepalives, but no live events.
