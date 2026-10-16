---
title: Teams
description: Create and manage teams, the groups of agents an IVR transfer rings, and their members.
---

A team is a group of agents that an IVR flow's transfer node rings together. A team has a ring strategy and queue settings. See [Agents & routing](/tring/features/agents-and-routing/) for how calls are routed.

:::note
Teams are identified by **name**. There is no team ID. The name is what a flow uses to target the team, so it's also the path parameter: `/v1/teams/{name}`. A name can't be changed after creation. URL-encode names that contain spaces or other special characters (`Tier 1` becomes `Tier%201`).
:::

All endpoints accept a tenant API key or a console session.

### The team object

| Field | Type | Description |
|---|---|---|
| `tenant_id` | UUID | Owning tenant. |
| `name` | string | Team name, unique within the tenant. |
| `strategy` | string | How agents are rung. See below. |
| `ring_seconds` | integer | How long to ring an agent before moving on. |
| `max_wait_seconds` | integer | How long a caller can wait in the team's queue. |
| `moh_sound` | string | What the caller hears while waiting. `local_stream://moh` (the default) means "no team override": the tenant's `hold_music` plays if set, otherwise the built-in music. |
| `created_at` | timestamp | When the team was created. |
| `members` | array | The team's agents. **Only in `GET /v1/teams/{name}`.** |

| `strategy` | Behaviour |
|---|---|
| `ring_all` | Ring every available agent at once. The default. |
| `sequential` | Ring agents one at a time, in `position` order. |
| `round_robin` | Ring one at a time, starting from a different agent on each call to spread the load. |
| `longest_wait` | Ring one at a time, starting with the agent who has been free the longest. |

Only members with `available_for_calls: true` who are connected are rung.

A **member** looks like this:

| Field | Type | Description |
|---|---|---|
| `agent_id` | UUID | Agent ID. |
| `sip_username` | string | Agent's `sip_username`. |
| `display_name` | string | Left out when empty. |
| `available_for_calls` | boolean | The agent's availability flag. |
| `position` | integer | Order within the team (lowest first). |

Members are sorted by `position`, then `sip_username`.

### POST /v1/teams

Create a team.

**Auth:** API key or console session.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Unique within the tenant. Can't be changed later. |
| `strategy` | string | no | `ring_all`, `round_robin`, `sequential` or `longest_wait`. Default `ring_all`. |
| `ring_seconds` | integer | no | Default `20`. Zero or negative values use the default. |
| `max_wait_seconds` | integer | no | Default `120`. Zero or negative values use the default. |
| `moh_sound` | string | no | A sound's `file_path` from the [sound library](/tring/api-reference/audio/) to override the tenant's hold music for this team. Default `local_stream://moh`, which uses the tenant's [`hold_music`](/tring/api-reference/tenant/#the-tenant-object). |

```bash
curl -sS -X POST "$TRING_URL/v1/teams" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"name":"support","strategy":"longest_wait","ring_seconds":15,"max_wait_seconds":300}'
```

**Response** `201 Created`

```json
{
  "tenant_id": "00000000-0000-4000-8000-000000000001",
  "name": "support",
  "strategy": "longest_wait",
  "ring_seconds": 15,
  "max_wait_seconds": 300,
  "moh_sound": "local_stream://moh",
  "created_at": "2026-10-16T09:41:07.512843+05:30"
}
```

**Errors**

| Status | `error` |
|---|---|
| `400` | `invalid JSON body` |
| `400` | `name required` |
| `400` | `strategy must be ring_all\|round_robin\|sequential\|longest_wait` |
| `409` | `a team with this name already exists` |
| `500` | `create failed` |

### GET /v1/teams

List all teams, sorted by name. Not paginated, and members aren't included.

**Auth:** API key or console session.

```bash
curl -sS "$TRING_URL/v1/teams" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response** `200 OK`

```json
[
  {
    "tenant_id": "00000000-0000-4000-8000-000000000001",
    "name": "billing",
    "strategy": "ring_all",
    "ring_seconds": 20,
    "max_wait_seconds": 120,
    "moh_sound": "local_stream://moh",
    "created_at": "2026-10-14T11:00:00.000000+05:30"
  },
  {
    "tenant_id": "00000000-0000-4000-8000-000000000001",
    "name": "support",
    "strategy": "longest_wait",
    "ring_seconds": 15,
    "max_wait_seconds": 300,
    "moh_sound": "local_stream://moh",
    "created_at": "2026-10-16T09:41:07.512843+05:30"
  }
]
```

**Errors**

| Status | `error` |
|---|---|
| `500` | `query failed`, `scan failed` |

### GET /v1/teams/{name}

Get one team with its members.

**Auth:** API key or console session.

```bash
curl -sS "$TRING_URL/v1/teams/support" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response** `200 OK`

```json
{
  "tenant_id": "00000000-0000-4000-8000-000000000001",
  "name": "support",
  "strategy": "longest_wait",
  "ring_seconds": 15,
  "max_wait_seconds": 300,
  "moh_sound": "local_stream://moh",
  "created_at": "2026-10-16T09:41:07.512843+05:30",
  "members": [
    {
      "agent_id": "9a3e5d10-0000-4000-8000-00000000c001",
      "sip_username": "agent-1042",
      "display_name": "Asha Example",
      "available_for_calls": true,
      "position": 1
    }
  ]
}
```

`members` is left out when the team has no agents.

**Errors**

| Status | `error` |
|---|---|
| `404` | `team not found` |
| `500` | `query failed`, `member lookup failed` |

### PATCH /v1/teams/{name}

Update a team's settings. Fields you leave out keep their current value. The name can't be changed.

**Auth:** API key or console session.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `strategy` | string | no | `ring_all`, `round_robin`, `sequential` or `longest_wait`. |
| `ring_seconds` | integer | no | Ring time per agent, in seconds. |
| `max_wait_seconds` | integer | no | Maximum queue wait, in seconds. |
| `moh_sound` | string | no | Hold sound for the queue. |

:::caution
Unlike create, `PATCH` doesn't apply defaults or range checks to `ring_seconds` and `max_wait_seconds`. Send positive values.
:::

```bash
curl -sS -X PATCH "$TRING_URL/v1/teams/support" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"strategy":"sequential","ring_seconds":25}'
```

**Response** `200 OK` with the updated team, without `members`.

**Errors**

| Status | `error` |
|---|---|
| `400` | `invalid JSON body` |
| `400` | `strategy must be ring_all\|round_robin\|sequential\|longest_wait` |
| `404` | `team not found` |
| `500` | `update failed` |

### DELETE /v1/teams/{name}

Delete a team. Its memberships are removed with it. The agents themselves are not deleted.

**Auth:** API key or console session.

```bash
curl -sS -X DELETE "$TRING_URL/v1/teams/billing" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response** `204 No Content`

**Errors**

| Status | `error` |
|---|---|
| `404` | `team not found` |
| `500` | `delete failed` |

:::caution
Flows target teams by name. Deleting a team that a flow still transfers to doesn't update the flow. Check your [flows](/tring/api-reference/flows/) first.
:::

### POST /v1/teams/{name}/agents

Add an agent to a team, or change its position if it's already a member. Safe to repeat.

**Auth:** API key or console session.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `agent_id` | string | yes | The agent's **UUID**. Unlike the agent routes, a `sip_username` isn't accepted here. |
| `position` | integer | no | Order within the team, used by `sequential`. Default `1`. Zero or negative values use the default. |

```bash
curl -sS -X POST "$TRING_URL/v1/teams/support/agents" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"agent_id":"9a3e5d10-0000-4000-8000-00000000c002","position":2}'
```

**Response** `200 OK` with the team's full, updated member list:

```json
[
  {
    "agent_id": "9a3e5d10-0000-4000-8000-00000000c001",
    "sip_username": "agent-1042",
    "display_name": "Asha Example",
    "available_for_calls": true,
    "position": 1
  },
  {
    "agent_id": "9a3e5d10-0000-4000-8000-00000000c002",
    "sip_username": "agent-1043",
    "available_for_calls": false,
    "position": 2
  }
]
```

**Errors**

| Status | `error` |
|---|---|
| `400` | `invalid JSON body` |
| `400` | `invalid agent_id` (not a UUID) |
| `400` | `agent not found in this tenant` |
| `404` | `team not found` |
| `500` | `query failed`, `add failed`, `member lookup failed` |

### DELETE /v1/teams/{name}/agents/{agentID}

Remove an agent from a team. The agent itself isn't deleted.

**Auth:** API key or console session.

| Path param | Description |
|---|---|
| `name` | Team name. |
| `agentID` | The agent's UUID. |

```bash
curl -sS -X DELETE "$TRING_URL/v1/teams/support/agents/9a3e5d10-0000-4000-8000-00000000c002" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response** `204 No Content`

**Errors**

| Status | `error` |
|---|---|
| `400` | `invalid agent id` |
| `404` | `agent not in team` (also returned when the team doesn't exist) |
| `500` | `remove failed` |
