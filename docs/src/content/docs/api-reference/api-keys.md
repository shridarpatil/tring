---
title: API keys
description: Create, list and revoke tenant API keys.
---

Tenant API keys (`tk_live_…`) authenticate your backend. See [Authentication](/tring/api-reference/authentication/) for how they're used. These endpoints accept a **full**-scope API key or a console session. In practice an admin usually manages keys from the console.

### Scopes

Every key has a scope. Give each backend the narrowest one it needs: a leaked `full` key can mint more keys, read trunk config and repoint webhooks.

| Scope | Can call |
|---|---|
| `calls` | `POST /v1/calls`, `POST /v1/calls/connect`, `GET /v1/calls[/{id}[/events\|/recording]]`, `POST /v1/calls/{id}/hold\|resume`, `POST /v1/guest-credentials`, `POST /v1/agents`, `GET /v1/agents[/{id}]`, `POST /v1/agents/{id}/availability`, `GET /v1/agents/{id}/token`, `GET /v1/teams[/{name}]`, `GET /v1/queues`, `GET /v1/stats/overview`, `GET /v1/numbers`, `GET /v1/flows[/{id}]`, `GET /v1/stream/dashboard` |
| `full` | Everything above, plus API keys, trunks, webhooks, tenant settings and guest config, sounds, number/flow/team changes, and the flow simulator. |

Any other endpoint called with a `calls` key returns `403` `this API key's scope does not allow this endpoint`. Keys created before scopes existed are `full`. A console session always has full access.

### The API key object

| Field | Type | Description |
|---|---|---|
| `id` | UUID | Key ID. Use it to revoke the key. |
| `name` | string | A label for the key. |
| `key` | string | The full key. **Only in the create response.** Never returned again. |
| `key_prefix` | string | The first 12 characters of the key, for telling keys apart. |
| `scope` | string | `calls` or `full`. See [Scopes](#scopes). |
| `created_at` | timestamp | When the key was created. |
| `last_used_at` | timestamp | When the key last authenticated a request. Left out if it has never been used. |
| `revoked` | boolean | `true` once the key has been revoked. |

### POST /v1/api-keys

Create a new API key.

**Auth:** `full` API key or console session.

**Request body** (optional)

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | no | A label, for example the integration that will use it. Surrounding spaces are trimmed. Defaults to `API key` when empty or missing. Names don't have to be unique. |
| `scope` | string | no | `calls` or `full`. Defaults to `full` over the API; the console defaults new keys to `calls`. |

```bash
curl -sS -X POST "$TRING_URL/v1/api-keys" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"name":"crm-backend-prod","scope":"calls"}'
```

**Response** `201 Created`

```json
{
  "id": "6f1d2c3b-0000-4000-8000-00000000a001",
  "name": "crm-backend-prod",
  "key": "tk_live_q3vxexampleexampleexampleexampleexample",
  "key_prefix": "tk_live_q3vx",
  "scope": "calls",
  "created_at": "2026-10-16T09:41:07.512843+05:30",
  "revoked": false
}
```

:::caution
Copy `key` now and put it in your secret manager. Tring stores only its hash and can't show it again.
:::

**Errors**

| Status | `error` |
|---|---|
| `400` | `scope must be full or calls` |
| `500` | `key generation failed` |
| `500` | `create failed` |

### GET /v1/api-keys

List all of the tenant's keys, newest first, including revoked ones. Neither the full key nor its hash is ever returned.

**Auth:** `full` API key or console session.

```bash
curl -sS "$TRING_URL/v1/api-keys" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response** `200 OK`

```json
[
  {
    "id": "6f1d2c3b-0000-4000-8000-00000000a001",
    "name": "crm-backend-prod",
    "key_prefix": "tk_live_q3vx",
    "scope": "calls",
    "created_at": "2026-10-16T09:41:07.512843+05:30",
    "last_used_at": "2026-10-16T10:02:55.118003+05:30",
    "revoked": false
  },
  {
    "id": "6f1d2c3b-0000-4000-8000-00000000a000",
    "name": "API key",
    "key_prefix": "tk_live_m7ra",
    "scope": "full",
    "created_at": "2026-09-01T12:00:00.000000+05:30",
    "revoked": true
  }
]
```

The list is not paginated.

**Errors**

| Status | `error` |
|---|---|
| `500` | `query failed`, `scan failed` |

### DELETE /v1/api-keys/{id}

Revoke a key. Requests made with it fail with `401` from then on. Revoking is permanent, but the key stays in the list with `revoked: true` for auditing.

**Auth:** `full` API key or console session.

| Path param | Description |
|---|---|
| `id` | The key's `id` (not the key itself). |

```bash
curl -sS -X DELETE "$TRING_URL/v1/api-keys/6f1d2c3b-0000-4000-8000-00000000a000" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response** `204 No Content`

**Errors**

| Status | `error` |
|---|---|
| `400` | `invalid id` |
| `404` | `key not found or already revoked` |
| `500` | `revoke failed` |

:::caution
Nothing stops a key from revoking itself. If you revoke the key you're calling with, your next request with it fails. To rotate, create the new key, deploy it everywhere, check the old key's `last_used_at` has stopped moving, then revoke the old one.
:::
