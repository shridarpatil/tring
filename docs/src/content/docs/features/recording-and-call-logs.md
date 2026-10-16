---
title: Recording and call logs
description: Per-tenant stereo call recording with optional S3 storage, call records, per-call event timelines and the live dashboard stream.
---

Every call, from any source (web, phone, WhatsApp), gets one record in the same call log. Recording, the event timeline and the dashboard all build on it.

## Recording

Recording is a **per-tenant** setting: **Settings → Record calls** in the console, or `record_calls` on `PATCH /v1/tenants/me`. New tenants record by default.

```bash
curl -sS -X PATCH https://api.example.com/v1/tenants/me \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"record_calls": false}'
```

How it works:

- The setting is read when a call **starts**; changing it does not affect calls in progress.
- Capture begins when an **agent answers** (or the number dialed by an IVR transfer answers). The IVR, ringing and queue time are not recorded, so a call nobody answered has no recording.
- Each leg is captured as-is (Opus, no re-encoding) during the call. At hangup the legs are merged into **one stereo Ogg/Opus file: agent on the left channel, customer on the right**. If a call was transferred between agents, every agent leg is placed on the left channel at the moment it joined.
- Merging needs `ffmpeg` on the gateway host (it ships in the images).
- The file is named after the call's `leg_id` and written to `TRING_RECORDINGS_DIR`. When `TRING_S3_BUCKET` is set, it is uploaded to `<prefix>/<tenant-id>/YYYY/MM/DD/<leg-id>.ogg` and the local copy is deleted.

Fetch a recording through the API, which streams it from local disk or S3 (no public or pre-signed URLs):

```bash
curl -sS -o call.ogg https://api.example.com/v1/calls/<call-id>/recording \
  -H "Authorization: Bearer $TRING_API_KEY"
```

`404` means the call is not yours, or no recording exists (yet). A call only advertises `recording_url` once a file was actually produced.

:::caution[Personal data]
Recordings contain personal data. Tell callers they are recorded where the law requires it, keep the S3 bucket private and encrypted, grant the role only the access it needs, and set a retention policy (for example an S3 lifecycle rule). See [Configuration](/tring/getting-started/configuration/#6-call-recording-storage).
:::

## Call records

`GET /v1/calls` lists calls, newest first; `GET /v1/calls/{id}` returns one. See the [Calls API](/tring/api-reference/calls/).

| Field | What it is |
|---|---|
| `id`, `tenant_id` | Identifiers. |
| `direction` | `inbound` or `outbound`. |
| `status` | `ringing`, `answered`, `ended` (`failed` exists in the schema). |
| `source` | `web`, `phone` or `whatsapp`. Calls arriving through the SIP plug are recorded as `phone`. |
| `from_number`, `to_number` | Caller and destination. For a web call, `from_number` is the `client_ref` (or the visitor's display name, or `Web visitor`) and `to_number` is the flow's name. |
| `agent_id`, `agent_name` | The agent who answered. |
| `client_ref` | Your verified caller identity from `POST /v1/guest-credentials`. |
| `leg_id` | Opaque per-call leg id; also the recording file name. |
| `started_at`, `answered_at`, `ended_at`, `duration_seconds` | Timing. |
| `hangup_cause` | For gateway calls: `client` (caller hung up), `agent`, or `system` (IVR hangup, no answer, flow end, connection lost); `caller_hangup` when a phone caller hung up. |
| `recording_url` | Set when a recording exists. |
| `ivr_path` | The IVR journey: an ordered list of steps with `node_id`, `node_type`, `action`, `detail`, `dtmf_input`, `outcome` and `at` (when the step ran). |
| `room_id` | The gateway room, for live calls that can be supervised. |

List options:

| Query | What it does |
|---|---|
| `status` | Only calls with this status. |
| `active=true` | Only in-progress calls, oldest first, unpaginated. |
| `page`, `limit` | Numbered pages (`limit` defaults to 25, max 200). Without `page`, the first 50 calls (or `limit`) are returned. |

Responses are an envelope: `{ "items": [...], "total": n, "limit": n, "offset": n }`.

## Call events

`GET /v1/calls/{id}/events` returns the call's timeline in order, each as `{ "type", "at", "detail" }`:

| Type | When |
|---|---|
| `started` | The call record was opened. |
| `ring` | An agent (or team) was rung. Ring-all calls produce one per agent. |
| `no_answer` | A ring round ended unanswered. |
| `answered` | An agent answered (`detail` has the agent's name and id). |
| `transfer` | An agent blind-transferred the call. |
| `supervise` | A supervisor joined, switched mode, or left. |
| `ended` | The call ended (`detail` has `cause` and `duration_seconds`). |

Calls from before the timeline existed return an empty list.

## Live dashboard

The console's dashboard uses a Server-Sent Events stream, available to your own tools too:

```bash
curl -sS -N https://api.example.com/v1/stream/dashboard \
  -H "Authorization: Bearer $TRING_API_KEY"
```

1. The first frame is `snapshot`: live calls, agents with status, queues and today's stats.
2. Then live frames, each `event: <type>` with JSON `data`: `call.ringing`, `call.routing`, `call.answered`, `call.ended`, `call.transferred`, `call.supervised`, `agent.presence`, `agent.status`, plus recomputed `stats` and `queues`.
3. A `: ping` comment every 20 seconds keeps proxies from closing the connection. On reconnect you get a fresh snapshot.

The live frames travel over Redis from the gateway; without Redis you only get the snapshot. If you put nginx in front, the response already sets `X-Accel-Buffering: no`.

`GET /v1/stats/overview` returns today's figures on their own: `calls_today`, `answered_pct`, `abandoned_today`, `failed_today`, `avg_wait_seconds`, `avg_talk_seconds` and 12 `hourly` buckets. `GET /v1/queues` returns queue depth. See [Queues and stats](/tring/api-reference/queues-and-stats/).

These dashboard events are for live views. To react to calls in your systems, use [webhooks](/tring/features/webhooks/).
