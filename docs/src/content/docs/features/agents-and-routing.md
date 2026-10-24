---
title: Agents and routing
description: Agents, presence and availability, teams, ring strategies, queues and ring reservations.
---

When an IVR [Transfer node](/tring/features/ivr-flows/#transfer) or an agent's blind transfer needs a human, the gateway's routing engine picks who rings. This page explains the pieces it uses.

## Agents

An agent is a person who takes calls in the browser through the [agent widget](/tring/sdk/agent-widget/) or the [JavaScript API](/tring/sdk/javascript-api/).

| Field | What it is |
|---|---|
| `id` | Agent UUID. |
| `sip_username` | Your identifier for the agent, unique per tenant. Use your own CRM user id here: every `/v1/agents/{id}` path accepts either the UUID or the `sip_username`. It is also what a Transfer node's `agent_sip` names. |
| `display_name` | Name shown in the console, transfer pickers and call logs. |
| `available_for_calls` | Your Away/Break switch, controlled from your backend. |
| `status` | Live state on list responses: `online`, `on_call`, `away` or `offline`. |

Create agents in the console (**Agents**) or with `POST /v1/agents` (`sip_username` required, `display_name` optional). See the [Agents API](/tring/api-reference/agents/).

## Presence and availability

Two separate things decide whether an agent can be rung:

| | Controlled by | Meaning |
|---|---|---|
| **Presence** | The agent's browser | The widget holds a presence socket to the gateway. Connected and idle is `online`; in a call is `on_call`; disconnected is `offline`. Stored in Redis with a 30-second TTL, refreshed every 10 seconds, so a crashed browser drops out within 30 seconds. |
| **Availability** | Your backend | `available_for_calls`. Set it with `POST /v1/agents/{id}/availability` and `{"available": false}` when the agent goes on break. |

```bash
curl -sS -X POST https://api.example.com/v1/agents/crm-user-17/availability \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"available": false}'
```

Each change updates the live dashboard. An agent who is connected but unavailable shows as `away`.

:::caution
Availability is applied when Tring builds a **team**'s member list: unavailable members are not rung. A transfer to "all available agents" or to one specific agent checks presence only (connected and not on a call), not `available_for_calls`. If you rely on Away for those targets, route through a team.
:::

Presence needs Redis (`TRING_REDIS_ADDR`). Without it, the console cannot show who is online.

## Teams

A team is a named group of agents with routing settings. A Transfer node targets it by **name**.

| Field | Default | What it does |
|---|---|---|
| `name` | | Unique per tenant; used in flows and API paths. |
| `strategy` | `ring_all` | How members are rung. See below. |
| `ring_seconds` | `20` | How long each ring attempt lasts. |
| `max_wait_seconds` | `120` | Total time a caller can wait for this team before the transfer gives up (`no_answer`). |
| `moh_sound` | `local_stream://moh` | What waiting callers hear. A sound-library file path overrides; the default value means "use the tenant's hold music". |

Members have a `position` (default `1`), which orders them for `sequential`. Add members with `POST /v1/teams/{name}/agents` and `{"agent_id": "…", "position": 2}`. See the [Teams API](/tring/api-reference/teams/).

## Ring strategies

| Strategy | Who rings |
|---|---|
| `ring_all` | Every available member at once. The first to accept gets the call. |
| `round_robin` | One member at a time, starting from a rotating offset per team. Spreads load. |
| `sequential` | One member at a time, in `position` order. |
| `longest_wait` | One member at a time, the one idle longest first (by when they last finished a call). |

For the one-at-a-time strategies, each attempt rings one agent for `ring_seconds`. If they decline, the next agent rings at once; if they do not answer, their ring is withdrawn and the next agent rings. An agent who declines is not rung again for that call.

The round-robin counter is kept in memory per gateway process and resets on restart.

## Queues

When everyone on a team is busy, the caller waits in the team's queue:

1. The caller joins the line and hears the team's hold music (silence if no playable file is configured).
2. When an agent is free **and** the caller is at the head of the line, the agents are rung by the strategy.
3. If nobody answers, the caller goes back in line and waits again.
4. When `max_wait_seconds` runs out, the transfer reports `no_answer` and the flow takes that edge.

A caller who hangs up leaves the queue. Live queue depth per team is published to Redis and shown on the dashboard, and returned by `GET /v1/queues` together with the number of calls currently connected to an agent. See [Queues and stats](/tring/api-reference/queues-and-stats/).

## Reservations

Presence only flips to `on_call` when an agent **answers**. Without extra care, two callers leaving the queue at the same moment could both ring the same idle agent. To prevent this, the engine **reserves** each agent it is ringing for the ring time plus 5 seconds. A reserved agent is skipped by other calls until the ring ends. Reservations live in Redis, so every gateway sharing that Redis respects them; without Redis they are in-memory per process.

## Transfers without a team

| Transfer target | Behaviour |
|---|---|
| All available agents | Rings every connected, idle agent of the tenant at once. No queue settings apply; the default wait is 2 minutes and each ring attempt is 30 seconds. |
| One agent (`agent_sip`) | Rings that agent, with the same defaults. The caller hears the tenant's hold music while waiting. |

## What the agent sees

The ringing agent sees a label, never an internal id: the Transfer node's `label` if set, otherwise the caller's identity (phone number or `client_ref`), otherwise `Web Caller`. Use `label` to hide the caller's number from agents (see [Outbound calls](/tring/features/outbound-calls/#number-masking)).
