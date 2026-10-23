---
title: Numbers
description: Register phone numbers (DIDs) with a tenant, attach them to trunks and IVR flows, and control outbound caller-ID priority.
---

A **number** is a phone number (DID) your tenant holds. What it does depends on its type:

| Type | What it's for |
|---|---|
| `inbound` | A number customers dial. When a call arrives on it, Tring runs the [IVR flow](/tring/features/ivr-flows/) attached through `flow_id`. |
| `outbound` | A number Tring presents as caller ID on outbound calls. It must reference a [trunk](/tring/api-reference/trunks/), which is the route the call leaves on. Outbound numbers are also how Tring picks trunks: each active outbound number on an active trunk is one candidate route, ordered by `priority`. |
| `masking` | A number from the pool used for masked (anonymised) calling. |

All three types count as "numbers the tenant owns" when Tring checks an explicit `caller_id` on [`POST /v1/calls`](/tring/api-reference/calls/#post-v1calls).

All routes on this page take a tenant API key or a console session. See [Authentication](/tring/api-reference/authentication/).

## The number object

| Field | Type | Description |
|---|---|---|
| `id` | UUID | Number ID. |
| `tenant_id` | UUID | Owning tenant. |
| `e164` | string | The number, stored exactly as you sent it. |
| `type` | string | `inbound`, `outbound` or `masking`. |
| `trunk_id` | UUID or `null` | The trunk outbound calls from this number use. |
| `flow_id` | UUID or `null` | The IVR flow inbound calls to this number run. |
| `provider_did_id` | string | Your carrier's identifier for this DID, if you set one. Omitted when empty. |
| `priority` | integer | Lower is preferred. Orders outbound trunk selection. Default `100`. |
| `active` | boolean | Inactive outbound numbers are skipped when picking a trunk and can't be used as an explicit `caller_id`. Inactive inbound numbers stop receiving calls. |
| `created_at` | timestamp | When the number was registered. |

```json
{
  "id": "0b6f2a4e-1111-4c1d-9a00-000000000001",
  "tenant_id": "7d3e9f10-0000-4000-8000-000000000001",
  "e164": "+919800000001",
  "type": "inbound",
  "trunk_id": null,
  "flow_id": "5c1a7b22-2222-4e3f-8b00-000000000001",
  "priority": 100,
  "active": true,
  "created_at": "2026-10-16T09:41:07.512843+05:30"
}
```

## Uniqueness and inbound matching

- A number is unique per tenant: the pair (`tenant_id`, `e164`) can only exist once. Registering the same `e164` twice in one tenant fails with `409`.
- An **inbound** number can belong to only one tenant on a deployment: registering an inbound number another tenant already holds returns `409`. Outbound and masking numbers are only unique within your tenant.
- When a phone call arrives, the gateway looks up the dialed number (the user part of the SIP `To` header) with an **exact string match** against `e164` among `inbound` numbers. There is no normalisation: `+919800000001`, `919800000001` and `09800000001` are three different strings. Register the number in exactly the format your carrier sends.
- If no `inbound` number matches, or the match has no `flow_id`, the gateway rejects the call.

:::caution
Deactivating an inbound number (`active: false`) stops it receiving calls.
:::

If duplicate inbound numbers exist from before the cross-tenant check, the number registered first gets the call. A number only ever runs a flow of its own tenant.

### POST /v1/numbers

Register a number with your tenant.

**Auth:** tenant API key or console session.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `e164` | string | yes | The number: digits with an optional leading `+`, 3 to 15 digits. Stored as sent, not normalised. Use E.164 (`+919800000001`) unless your carrier sends inbound calls in another format (see above). |
| `type` | string | yes | `inbound`, `outbound` or `masking`. |
| `trunk_id` | UUID | for `outbound` | The trunk this number dials out through. Must be one of your tenant's trunks. Required when `type` is `outbound`. Optional for the other types. |
| `flow_id` | UUID | no | IVR flow to run for inbound calls. Must be one of your tenant's flows. |
| `provider_did_id` | string | no | Your carrier's ID for the DID, for your own reference. |
| `priority` | integer | no | Lower is preferred. Defaults to `100`. |

New numbers start with `active: true`.

```bash
curl -sS -X POST "$TRING_URL/v1/numbers" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "e164": "+919800000002",
    "type": "outbound",
    "trunk_id": "9a4c6e80-3333-4f00-8c00-000000000001",
    "priority": 10
  }'
```

**Response:** `201 Created` with the number object.

```json
{
  "id": "0b6f2a4e-1111-4c1d-9a00-000000000002",
  "tenant_id": "7d3e9f10-0000-4000-8000-000000000001",
  "e164": "+919800000002",
  "type": "outbound",
  "trunk_id": "9a4c6e80-3333-4f00-8c00-000000000001",
  "flow_id": null,
  "priority": 10,
  "active": true,
  "created_at": "2026-10-16T09:45:12.104512+05:30"
}
```

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid JSON body` |
| `400` | `e164 and type are required` |
| `400` | `e164 must be digits with an optional leading + (3-15 digits)` |
| `400` | `type must be inbound\|outbound\|masking` |
| `400` | `trunk_id is required for outbound numbers` |
| `400` | `trunk not found in this tenant` / `flow not found in this tenant`: the ID doesn't exist or belongs to another tenant |
| `409` | `number is already in use`: another tenant already has this inbound number |
| `409` | `number already registered for this tenant: …` followed by the database error. Returned when the insert fails, usually because you already have this number. |
| `500` | `lookup failed` |

### GET /v1/numbers

List every number in your tenant. Not paginated.

**Auth:** tenant API key or console session.

**Query parameters:** none.

Results are sorted by `type`, then `priority` (lowest first), then newest first.

```bash
curl -sS "$TRING_URL/v1/numbers" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `200 OK` with a JSON array of number objects (`[]` when there are none).

```json
[
  {
    "id": "0b6f2a4e-1111-4c1d-9a00-000000000001",
    "tenant_id": "7d3e9f10-0000-4000-8000-000000000001",
    "e164": "+919800000001",
    "type": "inbound",
    "trunk_id": null,
    "flow_id": "5c1a7b22-2222-4e3f-8b00-000000000001",
    "priority": 100,
    "active": true,
    "created_at": "2026-10-16T09:41:07.512843+05:30"
  },
  {
    "id": "0b6f2a4e-1111-4c1d-9a00-000000000002",
    "tenant_id": "7d3e9f10-0000-4000-8000-000000000001",
    "e164": "+919800000002",
    "type": "outbound",
    "trunk_id": "9a4c6e80-3333-4f00-8c00-000000000001",
    "flow_id": null,
    "priority": 10,
    "active": true,
    "created_at": "2026-10-16T09:45:12.104512+05:30"
  }
]
```

**Errors**

| Status | Message |
|---|---|
| `500` | `query failed` / `scan failed` |

### PATCH /v1/numbers/{id}

Change a number's priority, active flag or IVR flow. You can't change `e164`, `type` or `trunk_id`. Register a new number for that.

**Auth:** tenant API key or console session.

**Path parameters**

| Param | Description |
|---|---|
| `id` | Number UUID. |

**Request body.** Send at least one field.

| Field | Type | Required | Description |
|---|---|---|---|
| `priority` | integer | no | New priority. Lower is preferred. |
| `active` | boolean | no | `false` takes the number out of outbound trunk selection without deleting it. |
| `flow_id` | UUID | no | Attach this IVR flow. |
| `clear_flow` | boolean | no | `true` detaches the flow (sets `flow_id` to `null`). Wins over `flow_id` if you send both. |

Sending `"flow_id": null` does nothing. Use `clear_flow` to detach a flow.

How priority and `active` drive outbound routing depends on the tenant's `outbound_strategy` (`failover`, `round_robin` or `manual`). With `manual`, only the lowest-priority active outbound number is used, so you switch primary trunks by flipping `active` or `priority` here. See [Numbers & trunks](/tring/features/numbers-and-trunks/).

```bash
curl -sS -X PATCH "$TRING_URL/v1/numbers/0b6f2a4e-1111-4c1d-9a00-000000000001" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"flow_id": "5c1a7b22-2222-4e3f-8b00-000000000002"}'
```

**Response:** `200 OK` with the updated number object.

```json
{
  "id": "0b6f2a4e-1111-4c1d-9a00-000000000001",
  "tenant_id": "7d3e9f10-0000-4000-8000-000000000001",
  "e164": "+919800000001",
  "type": "inbound",
  "trunk_id": null,
  "flow_id": "5c1a7b22-2222-4e3f-8b00-000000000002",
  "priority": 100,
  "active": true,
  "created_at": "2026-10-16T09:41:07.512843+05:30"
}
```

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid id` |
| `400` | `invalid JSON body` |
| `400` | `at least one of priority, active, flow_id, or clear_flow is required` |
| `400` | `flow not found in this tenant` |
| `404` | `number not found` |
| `409` | `cannot deactivate the last active outbound trunk for this tenant; register another outbound number first`. Returned when you set `active: false` on the only active outbound number. Without it, every outbound call would fail. |
| `500` | `lookup failed: …` / `update failed: …` |

There is no `DELETE` endpoint for numbers.
