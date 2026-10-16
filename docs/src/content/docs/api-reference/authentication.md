---
title: Authentication
description: The credentials the Tring API accepts (tenant API keys, console sessions, agent tokens and visitor call tokens) and which routes need which.
---

Tring uses bearer credentials. Send them in the `Authorization` header:

```
Authorization: Bearer <credential>
```

If there's no `Authorization: Bearer` header, the API reads the `X-API-Key` header instead. Both carry the same credential. `Authorization` is the usual choice.

There are four kinds of credential. Your backend holds the first two. Browsers only ever get the last two, which are short-lived and minted by your backend.

| Credential | Looks like | Lifetime | Who holds it | Used for |
|---|---|---|---|---|
| **Tenant API key** | `tk_live_…` | Until revoked | Your backend | The whole tenant-scoped API |
| **Console session** | JWT | 12 hours | An admin console user | The whole tenant-scoped API, plus console-only routes |
| **Agent token** | JWT | 5 minutes | The agent's browser (agent widget) | Connecting to the media gateway, and the agent's own call control |
| **Visitor call token** | JWT | 2 minutes, single use | The visitor's browser (visitor widget) | Placing one web call at the media gateway |

## Tenant API keys

API keys are how your backend (CRM, support portal, scripts) talks to Tring. A key belongs to one tenant and has full access to that tenant's data through the API.

```bash
curl -sS "$TRING_URL/v1/agents" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

- Keys start with `tk_`. The API uses that prefix to tell a key apart from a console token.
- Tring stores only a SHA-256 hash of each key. The full key is shown **once**, when you create it. If you lose it, create a new one and revoke the old one.
- The first 12 characters (`key_prefix`, for example `tk_live_q3vx`) are kept so you can tell keys apart in lists.
- Each successful use updates the key's `last_used_at`.
- A revoked key stops working on the next request.

Create keys in the console under **API keys**, or with [`POST /v1/api-keys`](/tring/api-reference/api-keys/#post-v1api-keys). Revoke them with [`DELETE /v1/api-keys/{id}`](/tring/api-reference/api-keys/#delete-v1api-keysid). Creating a key needs an existing credential, so the first key is usually created from the console after signing in.

## Console sessions

The admin console signs in with email and password and gets back a JWT. The console then uses that token exactly like an API key. You can do the same from a script if you need to act as a console user.

### POST /v1/console/login

Exchange a console user's email and password for a session token. No authentication.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `email` | string | yes | The user's email. Matched case-insensitively, surrounding spaces trimmed. |
| `password` | string | yes | The user's password. |

```bash
curl -sS -X POST "$TRING_URL/v1/console/login" \
  -H 'Content-Type: application/json' \
  -d "{\"email\":\"admin@example.com\",\"password\":\"$TRING_ADMIN_PASSWORD\"}"
```

**Response** `200 OK`

```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.example.signature",
  "expires_at": "2026-10-16T21:41:07.512843+05:30",
  "tenant": {
    "id": "00000000-0000-4000-8000-000000000001",
    "name": "Default",
    "sip_domain": "t_default.tring.local",
    "outbound_strategy": "failover",
    "failover_causes": ["NETWORK_OUT_OF_ORDER", "GATEWAY_DOWN"],
    "hold_music": "",
    "record_calls": false,
    "max_call_seconds": 0,
    "transfer_failed_message": ""
  }
}
```

:::note
The `tenant` object in the login response only fills in `id`, `name`, `sip_domain`, `outbound_strategy` and `failover_causes`. The other settings come back as empty or zero values. Call [`GET /v1/tenants/me`](/tring/api-reference/tenant/#get-v1tenantsme) to read their real values.
:::

**Errors**

| Status | `error` |
|---|---|
| `400` | `invalid json` |
| `400` | `email and password required` |
| `401` | `invalid credentials` (wrong email or wrong password; the API doesn't say which) |
| `500` | `query failed`, `load tenant`, `issue token` |

The token is valid for 12 hours. There are no refresh tokens: sign in again when it expires. Signing out of the console just discards the token on the client. The token stays valid until it expires.

The first tenant and console user are created on first start from `TRING_BOOTSTRAP_TENANT`, `TRING_ADMIN_EMAIL` and `TRING_ADMIN_PASSWORD` (see the [Quickstart](/tring/getting-started/quickstart/)).

### Console-only routes

A few routes act on behalf of a specific console **user**, so they reject API keys (an API key has no user):

| Route | Notes |
|---|---|
| `POST /v1/calls/{id}/supervise` | Join a live web call as a supervisor. The user also needs the supervise permission, or gets `403 not permitted to supervise`. See [Calls](/tring/api-reference/calls/). |

Sending an API key to one of these returns `401 console session required`. An invalid or expired token returns `401 invalid console session`.

## Agent tokens

The agent widget runs in your agent's browser. It must never see your API key. Instead, your backend mints a short-lived agent token with [`GET /v1/agents/{id}/token`](/tring/api-reference/agents/#get-v1agentsidtoken) and passes it to the browser.

```bash
curl -sS "$TRING_URL/v1/agents/agent-1042/token" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

The response carries the token, the gateway URL and the ICE servers, so the widget can connect with nothing else configured. The agent widget's `data-token-url` points at **your** endpoint that does this call after checking who the agent is. See the [agent widget](/tring/sdk/agent-widget/).

An agent token is valid for 5 minutes and is accepted by:

- the media gateway's agent WebSocket, and
- the agent self-service routes below, which act only on **that agent's own** current call.

| Route | Purpose |
|---|---|
| `POST /v1/sdk/calls/hold` | Put the agent's own active call on hold |
| `POST /v1/sdk/calls/resume` | Resume it |
| `GET /v1/sdk/transfer-targets` | List where the agent can transfer to |
| `POST /v1/sdk/calls/transfer` | Transfer the agent's own active call |

These routes return `401 invalid agent token` for a missing, expired or wrong-audience token. An API key or console token is **not** accepted on them, and an agent token is not accepted anywhere else in `/v1`. Agent tokens can't place outbound calls: origination is backend-only (`POST /v1/calls` with an API key).

## Visitor call tokens

The visitor widget lets a website visitor call you from the browser. Your backend mints the credentials with [`POST /v1/guest-credentials`](/tring/api-reference/tenant/#post-v1guest-credentials) using the API key, and hands the result to the visitor's browser.

The token:

- is pinned to one IVR flow, chosen by your backend, so the browser can't pick a different one,
- can carry a `client_ref` (your id for the caller) that is recorded on the call,
- is valid for 2 minutes and works **once**. The gateway burns it on first use, so a captured token can't be replayed.

The API itself never accepts a visitor token. Only the media gateway does. There is no unauthenticated way to mint one: if anonymous visitors can call, put a CAPTCHA or rate limit on your own endpoint before it calls Tring.

## Which routes need which credential

| Routes | API key | Console session | Agent token |
|---|---|---|---|
| `POST /v1/console/login` | no auth | no auth | no auth |
| All other `/v1/*` (tenant, API keys, agents, teams, queues, stats, stream, audio, numbers, calls, flows, trunks, webhooks, guest credentials) | yes | yes | no |
| `POST /v1/calls/{id}/supervise` | no | yes | no |
| `/v1/sdk/*` | no | no | yes |
| `GET /healthz` | no auth | no auth | no auth |

On the tenant-scoped routes, failures look like this:

| Status | `error` | When |
|---|---|---|
| `401` | `missing credentials` | No `Authorization: Bearer` and no `X-API-Key` header |
| `401` | `invalid credentials` | Unknown or revoked API key, or an invalid or expired console token |

## Security guidance

- **Keep API keys on the server.** Read them from an environment variable or a secret manager. Never put them in front-end code, mobile apps, or a repository.
- **Give browsers only minted tokens.** Agents get agent tokens and visitors get visitor call tokens. Both are short-lived and narrow in what they can do.
- **Check who the user is before you mint.** Your token endpoint is what decides who can become which agent, and which `client_ref` a visitor gets. Authenticate the user in your own app first.
- **Use one key per integration**, with a descriptive name, so you can revoke one without breaking the others. `last_used_at` shows whether a key is still in use.
- **Rotate keys regularly**, and right away if one may have leaked: create the new key, deploy it, then revoke the old one.
- **Change the defaults.** Set a strong, unique `TRING_JWT_SECRET` (it signs every console, agent and visitor token) and change `TRING_ADMIN_PASSWORD` before the API is reachable by anyone else. Changing `TRING_JWT_SECRET` invalidates every outstanding token.
- **Use TLS** between clients and the API. Bearer credentials sent over plain HTTP can be read in transit.
- **Restrict network access** to the console, and use MFA and audit logging on whatever sits in front of it (VPN, SSO proxy) where you can.
