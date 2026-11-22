---
title: Dispositions & call tags
description: The two label lists agents pick from when they wrap up a call, and the API a CRM uses to read and edit a call's outcome.
---

When a call ends, an agent can record how it went: one **disposition** (for example "Resolved"), any number of **tags** and free-text notes. Dispositions and tags are two lists the tenant manages. For how wrap-up works, see [Agents & routing](/tring/features/agents-and-routing/#call-wrap-up).

Both lists have the same shape and the same routes. Admins manage them in the console under **Dispositions & tags**.

## The label object

| Field | Type | Description |
|---|---|---|
| `id` | UUID | Label ID. |
| `name` | string | Display name. |
| `archived` | boolean | Archived labels are hidden from agents and can't be put on a call. Calls that already carry one keep it. |
| `created_at` | timestamp | |

Names are trimmed, 1 to 64 characters, and unique within the tenant ignoring case (`Resolved` and `resolved` clash). Labels are never deleted: archive one to retire it, restore it later.

## Routes

Replace `{list}` with `dispositions` or `call-tags`.

| Method | Path | Auth |
|---|---|---|
| `GET` | `/v1/{list}` | Tenant API key (any scope, including `calls`) or console session |
| `POST` | `/v1/{list}` | Full-scope API key or console session |
| `PATCH` | `/v1/{list}/{id}` | Full-scope API key or console session |

### GET /v1/{list}

List labels, oldest first. Archived ones are left out unless you pass `include_archived=1`.

```bash
curl -sS "$TRING_URL/v1/dispositions?include_archived=1" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `200 OK` with an array of label objects.

```json
[
  { "id": "d15b0a00-0000-4000-8000-000000000001", "name": "Resolved", "archived": false,
    "created_at": "2026-11-22T10:00:00Z" }
]
```

### POST /v1/{list}

**Request body:** `{"name": "Resolved"}`. **Response:** `201 Created` with the label.

| Status | `error` |
|---|---|
| `400` | `invalid JSON body` |
| `400` | `name required, max 64 characters` |
| `409` | `name already exists` |

### PATCH /v1/{list}/{id}

Rename or archive a label. Send either field or both: `{"name": "Sorted"}`, `{"archived": true}`. `{"archived": false}` restores it. **Response:** `200 OK` with the label.

| Status | `error` |
|---|---|
| `400` | `invalid JSON body` |
| `400` | `name required, max 64 characters` |
| `404` | `not found` |
| `409` | `name already exists` |

## Editing a call's outcome

### PATCH /v1/calls/{id}/wrap-up

Set or change a call's disposition, notes and tags from your backend, for example after your CRM's own wrap-up form. It works at any time, including long after the agent's wrap-up window closed, and on calls that were never wrapped up.

**Auth:** any tenant API key, including `calls`-scoped keys, or a console session. In the console only admins can edit an outcome.

**Request body**

| Field | Type | Description |
|---|---|---|
| `disposition_id` | UUID or `null` | A live disposition. `null` clears it. |
| `notes` | string or `null` | At most 2000 characters. `null` clears it. |
| `tag_ids` | UUID array | Replaces the whole tag set. At most 10 distinct tags; `[]` clears them. |

A field you leave out is unchanged. A field you send replaces the stored value. The patch never changes `wrapped_up_at` or `wrap_up_end`: a call that timed out stays `timeout`.

```bash
curl -sS -X PATCH "$TRING_URL/v1/calls/c0ffee00-6666-4a00-8f00-000000000003/wrap-up" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"disposition_id":"d15b0a00-0000-4000-8000-000000000001","notes":"Refund issued","tag_ids":[]}'
```

**Response:** `200 OK` with the [call object](/tring/api-reference/calls/#the-call-object).

| Status | `error` |
|---|---|
| `400` | `invalid id` / `invalid JSON body` / `invalid disposition_id` / `invalid notes` / `invalid tag_ids` |
| `400` | `notes too long (max 2000 characters)` |
| `400` | `too many tags (max 10)` |
| `400` | `unknown or archived disposition or tag` (including another tenant's) |
| `404` | `call not found` |

Every successful patch sends a [`call.wrapped_up`](/tring/api-reference/webhooks/#callwrapped_up) webhook if the call's flow picked one.

## Filtering the call log

[`GET /v1/calls`](/tring/api-reference/calls/#get-v1calls) takes `disposition_id` and `tag_id` to list calls with that outcome.
