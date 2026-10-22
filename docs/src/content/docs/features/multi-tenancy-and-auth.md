---
title: Multi-tenancy and auth
description: Tenants, and every credential Tring uses (API keys, console sessions, agent tokens, visitor call tokens, supervisor tokens), what each is for and how to keep them least-privilege.
---

## Tenants

A **tenant** is one organization. Every agent, team, number, trunk, flow, sound, webhook, API key, console user and call belongs to exactly one tenant, and every API query is filtered by the tenant of the credential that made it. A tenant cannot read or change another tenant's data, and gateway rooms are namespaced by tenant so two tenants can never share a call.

The first tenant and its console admin are created on first boot from `TRING_BOOTSTRAP_TENANT`, `TRING_ADMIN_EMAIL` and `TRING_ADMIN_PASSWORD` (see [Configuration](/tring/getting-started/configuration/#3-first-boot-bootstrap)). There is no API for creating more tenants or console users yet.

Tenant-wide settings (`GET` / `PATCH /v1/tenants/me`): `outbound_strategy`, `failover_causes`, `hold_music`, `record_calls`, `max_call_seconds` (60–86400, default 14400; also the lifetime of ephemeral TURN credentials) and `transfer_failed_message`. The default visitor flow lives at `/v1/guest-config`. See the [Tenant API](/tring/api-reference/tenant/).

Trunks are configured in the database per tenant, but they are all loaded into the one shared FreeSWITCH. Per-tenant trunk isolation and multi-region deployments are on the roadmap.

## Credentials at a glance

| Credential | Looks like | Issued by | Lifetime | Accepted by | Grants |
|---|---|---|---|---|---|
| **API key** | `tk_live_…` | Console (**API keys**) or `POST /v1/api-keys` | Until revoked | The api (`/v1/*`) | Full access to the tenant's API |
| **Console session** | JWT, `aud: tring.console` | `POST /v1/console/login` (email + password) | 12 hours | The api (`/v1/*`) | Same as an API key, plus supervision if the user is permitted |
| **Agent token** | JWT, `aud: tring.sdk` | `GET /v1/agents/{id}/token` (API key) | 5 minutes | Gateway `/rtc`; api `/v1/sdk/*` | Go online as that agent, take calls, control the agent's own call |
| **Visitor call token** | JWT, `aud: tring.gateway` | `POST /v1/guest-credentials` (API key) | 2 minutes, single use | Gateway `/rtc` | Join one call pinned to one flow |
| **Supervisor token** | JWT, `aud: tring.supervisor` | `POST /v1/calls/{id}/supervise` (console session only) | 2 minutes, single use | Gateway `/rtc` | Listen to, whisper on or barge into one live call |
| **FreeSWITCH basic auth** | `user:password` | `TRING_FS_BASIC_AUTH` | Static | The api (`/fs/*`) | FreeSWITCH directory lookups. Internal only |

All JWTs are HS256, signed with `TRING_JWT_SECRET`, with issuer `tring`. Each kind has its own audience and is rejected anywhere else: an agent token cannot call the tenant API, and a visitor token cannot register as an agent.

Send any credential as `Authorization: Bearer <credential>`. The api also accepts `X-API-Key: <key>`. The gateway also accepts `?token=<token>` on the `/rtc` WebSocket URL.

## API keys

For your **servers**: CRM backends, token endpoints, provisioning scripts.

- The full key is shown **once**, when it is created. Tring stores only its SHA-256 hash and a short prefix for display.
- Each use updates `last_used_at`, shown in the key list, which helps you find unused keys.
- Revoke with `DELETE /v1/api-keys/{id}`. A revoked key stops working immediately.
- Managing keys requires an existing credential of the same tenant (console session or another key).

```bash
export TRING_API_KEY="$(your-secret-manager get tring/api-key)"
curl -sS https://api.example.com/v1/agents -H "Authorization: Bearer $TRING_API_KEY"
```

:::danger
An API key can do everything in its tenant, including placing billable calls and reading recordings. Never put it in front-end code, mobile apps or source control. If one leaks, revoke it at once and create a new one. See the [API keys reference](/tring/api-reference/api-keys/) and [Authentication](/tring/api-reference/authentication/).
:::

## Console sessions

The admin console signs in with email and password (bcrypt-hashed) and holds a 12-hour session token. There is no refresh token; signing in again issues a new one. Failed logins return the same generic error whether the email or the password was wrong.

Only console sessions can supervise calls, and only for users with the supervise permission (the bootstrap admin has it; it is off by default for others).

## Agent tokens

Your backend mints an agent token for the agent who is signed in to **your** app, and passes it to their browser through your own endpoint (the agent widget's `data-token-url`). The token response also carries `gateway_url` and `ice_servers`, so the page needs nothing else.

```js
// Your server (Node.js, built-in fetch). Check YOUR session first.
app.get("/tring/token", requireLogin, async (req, res) => {
  const r = await fetch(
    `https://api.example.com/v1/agents/${encodeURIComponent(req.user.crmId)}/token`,
    { headers: { Authorization: `Bearer ${process.env.TRING_API_KEY}` } },
  );
  res.status(r.status).type("application/json").send(await r.text());
});
```

Mint tokens only for the user who is actually signed in, never for an id taken from the request. The 5-minute lifetime limits the damage of a leaked token; the widget fetches a new one when it needs to. See the [agent widget](/tring/sdk/agent-widget/).

With an agent token, a browser can only act as that agent: go online, accept or decline rings, and hold, resume or transfer **its own** call. It cannot place outbound calls or read anything else.

## Visitor call tokens

Your backend mints one per call with `POST /v1/guest-credentials` and an API key, after deciding the visitor may call (see [Web calling](/tring/features/web-calling/#visitor-calls)).

- The **flow is pinned** inside the token; the browser cannot choose another flow.
- `client_ref`, if you pass it, is stored on the call as a trusted identity. Pass only identities your backend verified.
- The gateway **burns** the token on first use (Redis), so a captured token cannot start a second call. A page refresh within the grace window reattaches with a separate session token instead.
- Visitor connections cannot transfer calls or dial numbers.

Because anyone can reach a public "call us" page, protect your credentials endpoint with rate limits and a CAPTCHA or session check before minting.

## Least-privilege guidance

- **One API key per backend service**, named after it, so you can revoke one without breaking others. Revoke keys whose `last_used_at` is stale.
- **Keep API keys server-side.** Browsers only ever receive agent, visitor and supervisor tokens, which are short-lived and narrowly scoped.
- **Gate your token endpoints** with your own authentication (agents) or abuse controls (visitors). Tring trusts whatever your backend asks for.
- **Grant supervision sparingly.** It allows silent listening to live calls.
- **Protect `TRING_JWT_SECRET`.** It signs every token for every tenant. Store it in a secret manager and rotate it if exposed (rotation signs everyone out).
- **Use HTTPS everywhere** in production: the api, the gateway (`wss://` for `/rtc`), your token endpoints and webhook receivers.
- **Keep internals private.** `/fs/*`, FreeSWITCH ESL, Postgres and Redis must not be reachable from the internet.
- **Turn on MFA and audit logging** for the cloud, registry and secret-manager accounts that hold these credentials.

:::caution[CORS]
The api currently reflects any `Origin` in CORS responses, so browsers on any site may call it. This is safe only because API keys never reach browsers; credentials are never sent automatically (no cookies). Do not change that.
:::
