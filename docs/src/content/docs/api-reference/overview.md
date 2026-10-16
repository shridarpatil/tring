---
title: API overview
description: Base URL, request and response conventions, errors, pagination and timeouts for the Tring REST API.
---

The Tring REST API is served by the `tring-api` container (port `8085` by default), on the same origin as the admin console (`/admin/`) and the browser SDK bundles (`/sdk/`). Everything your backend calls lives under the `/v1` prefix.

## Base URL

```
http://localhost:8085/v1        # local Docker Compose install
https://api.example.com/v1      # behind your own TLS proxy
```

The examples in this reference use two environment variables:

```bash
export TRING_URL=https://api.example.com
export TRING_API_KEY=...   # a tenant API key, see Authentication
```

So `"$TRING_URL/v1/agents"` is the agents collection.

:::caution
Put TLS in front of the API before it leaves localhost. API keys and console tokens are bearer credentials: anyone who sees one can use it.
:::

### Routes outside `/v1`

| Path | Auth | What it is |
|---|---|---|
| `GET /healthz` | none | Returns `{"status":"ok"}`, or `503` with `{"error":"db unreachable"}` when Postgres is down. Use it for load balancer health checks. |
| `/fs/*` | HTTP Basic (`TRING_FS_BASIC_AUTH`) | Used by FreeSWITCH only. Not for integrators. |
| `/sdk/*` | none | Browser widget bundles (`voice.js`, `agent.js`, `tring.js`). |
| `/admin/*` | none (the console signs in through the API) | The admin console. |

## Authentication

Every `/v1` route except `POST /v1/console/login` needs a credential in the `Authorization: Bearer …` header. Most routes take either a tenant API key (`tk_live_…`) or a console session token. A few take only an agent token or only a console session. See [Authentication](/tring/api-reference/authentication/) for which is which.

Everything is scoped to one tenant. The credential decides the tenant. No tenant ID goes in the path, and you can't read or change another tenant's data.

## Requests

- Send request bodies as JSON with `Content-Type: application/json`. The one exception is [audio upload](/tring/api-reference/audio/), which is `multipart/form-data`.
- Field names are `snake_case`.
- Unknown fields in a request body are ignored.
- For `PATCH` endpoints, a field you leave out keeps its current value. Send only the fields you want to change.
- IDs are UUIDs. Two exceptions: teams are addressed by their **name** (`/v1/teams/{name}`), and agent routes accept either the agent UUID or its `sip_username` (`/v1/agents/{id}`).

## Responses

- Successful responses are JSON (`Content-Type: application/json`), except file downloads (audio, recordings) and the [dashboard event stream](/tring/api-reference/queues-and-stats/#get-v1streamdashboard).
- `DELETE` endpoints return `204 No Content` with an empty body.
- Optional fields with no value may be left out of the response entirely, rather than sent as `null` or `""`. For example, `display_name` is omitted on an agent that has none.

### Timestamps

Timestamps are RFC 3339 strings, for example `2026-10-16T09:41:07.512843+05:30`. The UTC offset is the API server's or database's time zone, so parse them with a proper RFC 3339 parser rather than assuming `Z`.

## Errors

Every error the API itself returns uses the same shape: a JSON object with a single `error` string.

```json
{ "error": "sip_username required" }
```

Error messages are short, readable strings meant for logs and for showing to operators. They are not a stable machine contract. Branch on the HTTP status code, not the message text.

### Status codes

| Status | Meaning |
|---|---|
| `200 OK` | Success, with a JSON body. |
| `201 Created` | A resource was created. The body is the new resource. |
| `204 No Content` | Success, no body (deletes, revokes). |
| `400 Bad Request` | Bad JSON, a missing required field, or a value that fails validation. |
| `401 Unauthorized` | Missing, unknown, expired or revoked credentials. Common messages: `missing credentials`, `invalid credentials`, `invalid agent token`, `console session required`. |
| `403 Forbidden` | Authenticated, but this user isn't allowed to do this (for example, supervising without the permission). |
| `404 Not Found` | The resource doesn't exist in your tenant. |
| `409 Conflict` | A uniqueness rule or a state check failed (for example, a duplicate name). |
| `500 Internal Server Error` | A server-side failure. Some messages include the underlying database error. |
| `503 Service Unavailable` | A dependency or feature isn't available (for example, audio uploads with no audio directory configured). |
| `504 Gateway Timeout` | The request took longer than the 30-second limit (see below). |

:::note
A `504` from the request timeout, and a `500` from a recovered panic, come from the HTTP middleware, not the API handlers. Their body is not the `{"error": …}` JSON. Treat any non-2xx response as an error even if the body doesn't parse.
:::

## Pagination

Most list endpoints (API keys, agents, teams, audio, numbers, trunks, webhooks) return a plain JSON array of every item in your tenant, with no pagination.

The endpoints that can grow large, [calls](/tring/api-reference/calls/) and [flows](/tring/api-reference/flows/), return an envelope instead:

```json
{
  "items": [ ... ],
  "total": 1342,
  "limit": 25,
  "offset": 50
}
```

| Field | Description |
|---|---|
| `items` | The rows on this page. Always an array, never `null`. |
| `total` | Number of rows matching the query, before the page window was applied. |
| `limit` | Page size that was used. |
| `offset` | Number of rows skipped. |

Pass these query parameters to page through results:

| Param | Description |
|---|---|
| `page` | 1-based page number. Including `page` turns on numbered pagination. |
| `limit` | Page size. Defaults to `25` when `page` is set. Capped at `200`. |

```bash
curl -sS "$TRING_URL/v1/calls?page=3&limit=50" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

Without `page`, each endpoint has its own default. See the [calls](/tring/api-reference/calls/) and [flows](/tring/api-reference/flows/) pages.

## Timeouts and limits

- **Request timeout:** 30 seconds for every `/v1` route except `GET /v1/stream/dashboard`, which is a long-lived stream.
- **Header read timeout:** the server allows 5 seconds to receive request headers.
- **Upload size:** audio uploads are capped at 10 MB. Other endpoints have no special size limit.
- **Rate limits:** the API has no built-in rate limiting. If you expose an endpoint of your own that calls Tring for anonymous visitors (for example, minting [guest credentials](/tring/api-reference/tenant/#post-v1guest-credentials)), add your own rate limit or CAPTCHA in front of it.

## CORS

The API answers CORS preflight requests and echoes back any `Origin`. It allows the methods `GET, POST, PATCH, DELETE, OPTIONS` and the headers `Authorization, Content-Type, X-API-Key`. This is permissive by design for v0. It does **not** mean API keys belong in browsers: call the API from your backend, and give browsers only the short-lived tokens it mints.

## Versioning

The only version is `v1`, and it's in the path. There are no version headers. Write clients that ignore response fields they don't recognise, so new fields don't break them.
