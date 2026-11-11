---
title: Campaigns
description: Campaign definitions, recipient imports, preparation, scheduling, test calls, and results.
---

All endpoints require a tenant administrator console session or a `full` tenant API key. Every campaign, recipient, and attempt belongs to one tenant. See [Campaigns and voice blasts](/tring/features/campaigns/) for execution semantics.

## Create a draft

`POST /v1/campaigns` → `201` campaign.

```json
{
  "name": "Appointment reminders",
  "config": {
    "type": "tts",
    "text": "Hello. This is a reminder about your appointment tomorrow.",
    "caller_id": "+912265915000",
    "timezone": "Asia/Kolkata",
    "window_start": "09:00",
    "window_end": "18:00",
    "concurrency": 2,
    "calls_per_minute": 30,
    "max_attempts": 2,
    "retry_minutes": 30,
    "ring_seconds": 30,
    "max_seconds": 300
  }
}
```

For `type: "audio"`, provide `audio_id` from `POST /v1/audio`. For `type: "ivr"`, provide `flow_id` of an active flow. For `type: "agent"`, provide `team` (the tenant team name) and `agent_wait_seconds` (5–3,600). No audio, text or flow is required. Preparation validates the team and returns `prepared.team`. The team is revalidated at launch and execution. Agents answer in their Tring app/widget before the client is dialed. `webhook_id` optionally selects a registered active result webhook. All delivery fields above are required; `text`, `audio_id`, and `flow_id` are required only for their respective type. `team` and `agent_wait_seconds` are required for agent-assisted campaigns.

| Field | Limits |
|---|---|
| `name` | 1–200 bytes |
| `text` | 1–5,000 bytes, static text |
| `timezone` | Valid IANA timezone |
| `window_start`, `window_end` | `HH:MM`; end after start on the same day |
| `concurrency` | 1–100 |
| `calls_per_minute` | 1–300 |
| `max_attempts` | 1–5, including first attempt |
| `retry_minutes` | 1–10,080 |
| `ring_seconds` | 5–120 |
| `max_seconds` | 10–3,600 |

The response includes `id`, `name`, `status`, `config`, `prepared`, `revision`, `scheduled_at`, and timestamps. Detail responses also include `counts` (recipient counts by status) and `metrics` (`attempts`, `answered`, `completed`, excluding test calls).

## Read and edit

| Method and path | Result |
|---|---|
| `GET /v1/campaigns?page=1&limit=25` | Paginated campaigns. |
| `GET /v1/campaigns/{id}` | Campaign, recipient counts, and attempt metrics. |
| `PUT /v1/campaigns/{id}` | Replace a draft's `name` and `config`; supply its latest `revision`. Clears prepared content. |
| `POST /v1/campaigns/{id}/prepare` | Verify and snapshot content; body `{"revision": 3}`. Returns updated campaign. |

List endpoints use `{ "items": [], "total": 0, "limit": 25, "offset": 0 }`. Prepared `audio_file` paths can be played through the authenticated `GET /v1/audio/preview?path=...` endpoint. Prepared IVR nodes carry their copied audio paths.

Updates, preparation, lifecycle actions, and test calls use optimistic revisions. Fetch the latest campaign after a mutation. A stale revision or disallowed lifecycle transition returns `409`. Repeating a test or start request with the original revision does not place another call.

## Recipients

`POST /v1/campaigns/{id}/recipients` accepts JSON or multipart CSV for drafts only.

```json
{
  "recipients": [
    { "phone": "9876543210", "external_ref": "appointment-4821" },
    { "phone": "+919876543211" }
  ]
}
```

For CSV, send multipart field `file` with headers `phone,external_ref`. `external_ref` is optional and limited to 200 bytes. Maximum 10,000 rows and a 2 MB request per import. Numbers normalize using the same Indian mobile validation as outbound calls.

The response is `{ "added": 1, "duplicates": 1, "rejected": [{ "row": 3, "phone": "invalid", "error": "..." }] }`. Valid rows are imported; invalid rows are reported. Row numbers are one-based data rows, excluding the CSV header. Duplicates are checked both within the import and against existing campaign recipients.

`GET /v1/campaigns/{id}/recipients?page=1&limit=25&status=pending` returns a filtered recipient page. Each row has `id`, `phone`, `external_ref`, `status`, `attempts`, and `next_attempt_at`.

## Test and control

| Endpoint (`POST`) | Body / behavior |
|---|---|
| `/v1/campaigns/{id}/test` | `{"revision": 4, "to": "+919876543210"}`. Prepared draft only. Immediate real test call. `202 {"attempt_id":"..."}`. |
| `/v1/campaigns/{id}/start` | `{"revision": 4}`. Starts within the daily calling window. |
| `/v1/campaigns/{id}/schedule` | `{"revision": 4, "scheduled_at": "2026-12-01T09:00:00+05:30"}`. Future RFC3339 timestamp required. |
| `/v1/campaigns/{id}/pause` | `{"revision": 5}`. Stops new claims; active calls finish. |
| `/v1/campaigns/{id}/resume` | `{"revision": 6}`. Preserves a future scheduled start and calling hours. |
| `/v1/campaigns/{id}/cancel` | `{"revision": 7}`. Terminal; pending/unclaimed attempts are cancelled, active calls finish. |

Lifecycle actions return `200` with the updated campaign. Start and schedule require prepared content, at least one valid recipient, an owned active caller ID, and an active outbound trunk. Test calls bypass the calling window but share system capacity limits. Tests have `recipient_id: null` and never update recipient progress.

For agent-assisted campaigns, test calls ring the selected team first. If no agent accepts within the team/campaign wait, the attempt ends as `no_answer` with reason `agent_not_answered`, without dialing the client. It counts toward the maximum attempts and follows retry settings. `answered_at` records customer answer, never the agent's acceptance. `completed` means the conversation connected and ended normally.

## Results

`GET /v1/campaigns/{id}/attempts?page=1&limit=25` returns attempt history, newest first. Rows include `id`, `campaign_id`, `recipient_id`, `phone`, `external_ref`, `attempt`, `status`, `call_id`, `answered_at`, `finished_at`, `created_at`, and `error`. Use `GET /v1/calls/{call_id}` for the ordinary call log and timeline.

`GET /v1/campaigns/{id}/export` streams recipient results as CSV, with columns `recipient_id,phone,external_ref,status,attempts`. Spreadsheet-sensitive values are prefixed with an apostrophe.

Attempt statuses: `queued`, `active`, `completed`, `incomplete`, `busy`, `no_answer`, `failed`, `unknown`, `cancelled`. Recipient retries return to `pending`, preserving earlier attempts in history. `unknown` means the gateway outcome could not be confirmed and is not automatically retried.

The optional `campaign.attempt.finished` webhook includes:

```json
{
  "campaign_id": "...",
  "recipient_id": "...",
  "attempt_id": "...",
  "call_id": "...",
  "status": "completed",
  "reason": "",
  "answered": true,
  "external_ref": "appointment-4821",
  "test": false
}
```

This object is wrapped in the standard signed webhook envelope. As with existing IVR webhook submission, a gateway crash before submission can prevent a notification; reconcile through the attempt list. The IVR variables also include `campaign_id`, `recipient_id`, `attempt_id`, `external_ref`, `caller`, and `did` for callback and node-webhook integrations.
