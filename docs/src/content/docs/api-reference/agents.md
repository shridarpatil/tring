---
title: Agents
description: Create and list agents, set their availability, and mint agent tokens for the agent widget.
---

An agent is a person who takes calls in the [agent widget](/tring/sdk/agent-widget/). Agents are grouped into [teams](/tring/api-reference/teams/), and IVR flows route callers to them. See [Agents & routing](/tring/features/agents-and-routing/) for the concepts.

All endpoints accept a tenant API key or a console session.

:::note
Wherever a path has `{id}` on this page, you can pass either the agent's UUID **or** its `sip_username`. If you set `sip_username` to your own CRM user ID, you can call these endpoints with that ID directly, without looking up Tring's UUID first.
:::

### The agent object

| Field | Type | Description |
|---|---|---|
| `id` | UUID | Agent ID. |
| `tenant_id` | UUID | Owning tenant. |
| `sip_username` | string | The agent's username, unique in the tenant. Typically your CRM user ID. |
| `sip_password` | string | Generated SIP password. **Only in the create response.** |
| `display_name` | string | Name shown to supervisors and in the console. Left out when empty. |
| `available_for_calls` | boolean | Your backend's availability flag (your "Away" / "Break" button). Tring rings an agent only when they're connected **and** available. |
| `created_at` | timestamp | When the agent was created. |
| `status` | string | Live status. **Only in the list response.** See below. |

`status` combines whether the agent is connected with `available_for_calls`:

| `status` | Meaning |
|---|---|
| `online` | Connected and available. Can be rung. |
| `on_call` | Connected and busy on a call. |
| `away` | Connected, but `available_for_calls` is `false`. |
| `offline` | Not connected. |
| `unknown` | Only in the legacy SIP mode: Tring couldn't reach FreeSWITCH to check. |

### POST /v1/agents

Create an agent.

**Auth:** API key or console session.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `sip_username` | string | yes | Unique within the tenant. Letters, digits and `.` `_` `@` `+` `-`, up to 64 characters. Use your own user ID so you can address the agent by it. |
| `display_name` | string | no | Human-readable name. |

```bash
curl -sS -X POST "$TRING_URL/v1/agents" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"sip_username":"agent-1042","display_name":"Asha Example"}'
```

**Response** `201 Created`

```json
{
  "id": "9a3e5d10-0000-4000-8000-00000000c001",
  "tenant_id": "00000000-0000-4000-8000-000000000001",
  "sip_username": "agent-1042",
  "sip_password": "examplepasswordexample23",
  "display_name": "Asha Example",
  "available_for_calls": true,
  "created_at": "2026-10-16T09:41:07.512843+05:30"
}
```

The SIP password is generated for you. You don't need it with the agent widget, which authenticates with an [agent token](#get-v1agentsidtoken). It's shown only in this response.

A new agent isn't in any team yet. Add it with [`POST /v1/teams/{name}/agents`](/tring/api-reference/teams/#post-v1teamsnameagents).

**Errors**

| Status | `error` |
|---|---|
| `400` | `invalid JSON body` |
| `400` | `sip_username required` |
| `400` | `sip_username may only contain letters, digits and . _ @ + - (max 64)` |
| `409` | `sip_username already exists for this tenant` |
| `500` | `generate password`, `create agent failed` |

### GET /v1/agents

List all agents in the tenant, newest first, with their live `status`. Not paginated.

**Auth:** API key or console session.

```bash
curl -sS "$TRING_URL/v1/agents" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response** `200 OK`

```json
[
  {
    "id": "9a3e5d10-0000-4000-8000-00000000c001",
    "tenant_id": "00000000-0000-4000-8000-000000000001",
    "sip_username": "agent-1042",
    "display_name": "Asha Example",
    "available_for_calls": true,
    "created_at": "2026-10-16T09:41:07.512843+05:30",
    "status": "online"
  },
  {
    "id": "9a3e5d10-0000-4000-8000-00000000c002",
    "tenant_id": "00000000-0000-4000-8000-000000000001",
    "sip_username": "agent-1043",
    "available_for_calls": false,
    "created_at": "2026-10-15T18:20:00.000000+05:30",
    "status": "away"
  }
]
```

**Errors**

| Status | `error` |
|---|---|
| `500` | `query failed` |

### GET /v1/agents/{id}

Get one agent. This response has no `status` field: use the list, or the [dashboard stream](/tring/api-reference/queues-and-stats/#get-v1streamdashboard), for live status.

**Auth:** API key or console session.

| Path param | Description |
|---|---|
| `id` | Agent UUID or `sip_username`. |

```bash
curl -sS "$TRING_URL/v1/agents/agent-1042" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response** `200 OK`

```json
{
  "id": "9a3e5d10-0000-4000-8000-00000000c001",
  "tenant_id": "00000000-0000-4000-8000-000000000001",
  "sip_username": "agent-1042",
  "display_name": "Asha Example",
  "available_for_calls": true,
  "created_at": "2026-10-16T09:41:07.512843+05:30"
}
```

**Errors**

| Status | `error` |
|---|---|
| `404` | `agent not found` |
| `500` | `query failed` |

### POST /v1/agents/{id}/availability

Mark an agent available or unavailable for calls. Wire this to the Away / Break button in your own app, calling from your backend. An unavailable agent stays connected, but Tring stops ringing them.

**Auth:** API key or console session.

| Path param | Description |
|---|---|
| `id` | Agent UUID or `sip_username`. |

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `available` | boolean | yes | `true` to take calls, `false` to stop. A missing field counts as `false`. |

```bash
curl -sS -X POST "$TRING_URL/v1/agents/agent-1042/availability" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"available":false}'
```

**Response** `200 OK` with the updated agent (same shape as `GET /v1/agents/{id}`).

Each change also sends an `agent.status` [webhook](/tring/api-reference/webhooks/) and an `agent.status` event on the [dashboard stream](/tring/api-reference/queues-and-stats/#get-v1streamdashboard), with this data:

```json
{
  "agent_id": "9a3e5d10-0000-4000-8000-00000000c001",
  "sip_username": "agent-1042",
  "available_for_calls": false
}
```

**Errors**

| Status | `error` |
|---|---|
| `400` | `invalid JSON body` |
| `404` | `agent not found` |
| `500` | `query failed`, `update failed` |

### GET /v1/agents/{id}/token

Mint a short-lived agent token for the [agent widget](/tring/sdk/agent-widget/). Your backend calls this after checking that the signed-in user really is this agent, and returns the response to the agent's browser. The browser never sees your API key.

**Auth:** API key or console session.

| Path param | Description |
|---|---|
| `id` | Agent UUID or `sip_username`. |

```bash
curl -sS "$TRING_URL/v1/agents/agent-1042/token" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response** `200 OK`

```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.example.signature",
  "expires_at": "2026-10-16T09:46:07.512843+05:30",
  "gateway_url": "https://gw.example.com",
  "ice_servers": [
    { "urls": "stun:stun.example.com:3478" }
  ]
}
```

| Field | Description |
|---|---|
| `token` | The agent token (JWT). Valid for **5 minutes**. |
| `expires_at` | When the token expires. |
| `gateway_url` | Media gateway URL the widget connects to. Left out if `TRING_GATEWAY_PUBLIC_URL` isn't set. |
| `ice_servers` | STUN/TURN servers in `RTCIceServer` format. Left out if none are configured. |

Pass this response through unchanged from the endpoint your widget's `data-token-url` points at, and the widget needs no other configuration. The token is also accepted by the agent's own call-control routes (`/v1/sdk/*`). See [Authentication](/tring/api-reference/authentication/#agent-tokens).

**Errors**

| Status | `error` |
|---|---|
| `404` | `agent not found` |
| `500` | `query failed`, `issue token` |

:::caution
Whoever can call your token endpoint can act as that agent. Authenticate the user in your own app and only mint a token for the agent they actually are.
:::
