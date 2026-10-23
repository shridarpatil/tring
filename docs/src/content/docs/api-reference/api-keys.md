---
title: API keys
description: Create, list and revoke tenant API keys.
---

Tenant API keys (`tk_live_…`) authenticate your backend. See [Authentication](/tring/api-reference/authentication/) for how they're used. These endpoints accept an API key or a console session. In practice an admin usually manages keys from the console.

### The API key object

| Field | Type | Description |
|---|---|---|
| `id` | UUID | Key ID. Use it to revoke the key. |
| `name` | string | A label for the key. |
| `key` | string | The full key. **Only in the create response.** Never returned again. |
| `key_prefix` | string | The first 12 characters of the key, for telling keys apart. |
| `created_at` | timestamp | When the key was created. |
| `last_used_at` | timestamp | When the key last authenticated a request. Left out if it has never been used. |
| `revoked` | boolean | `true` once the key has been revoked. |

### POST /v1/api-keys

Create a new API key.

**Auth:** API key or console session.

**Request body** (optional)

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | no | A label, for example the integration that will use it. Surrounding spaces are trimmed. Defaults to `API key` when empty or missing. Names don't have to be unique. |

```bash
curl -sS -X POST "$TRING_URL/v1/api-keys" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"name":"crm-backend-prod"}'
```

**Response** `201 Created`

```json
{
  "id": "6f1d2c3b-0000-4000-8000-00000000a001",
  "name": "crm-backend-prod",
  "key": "tk_live_q3vxexampleexampleexampleexampleexample",
  "key_prefix": "tk_live_q3vx",
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
| `500` | `key generation failed` |
| `500` | `create failed` |

### GET /v1/api-keys

List all of the tenant's keys, newest first, including revoked ones. Neither the full key nor its hash is ever returned.

**Auth:** API key or console session.

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
    "created_at": "2026-10-16T09:41:07.512843+05:30",
    "last_used_at": "2026-10-16T10:02:55.118003+05:30",
    "revoked": false
  },
  {
    "id": "6f1d2c3b-0000-4000-8000-00000000a000",
    "name": "API key",
    "key_prefix": "tk_live_m7ra",
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

**Auth:** API key or console session.

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
