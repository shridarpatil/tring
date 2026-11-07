---
title: Users
description: Manage a tenant's users, their roles and console logins, and the logged-in user's own endpoints.
---

A user is anyone who signs in to the console or takes calls: agents, supervisors and admins share one table. See [Agents and routing](/tring/features/agents-and-routing/#roles-and-logins) for roles and team managers.

:::caution
`/v1/users*` accepts **only an admin console session**. An API key gets `401`, and a supervisor or agent session gets `403`. A leaked API key therefore cannot create admin logins.
:::

### The user object

| Field | Type | Description |
|---|---|---|
| `id` | UUID | User ID. |
| `display_name` | string | Name shown in the console and call logs. |
| `email` | string | Login email. Empty when the user has no login. |
| `role` | string | `admin`, `supervisor` or `agent`. |
| `sip_username` | string | Agent identifier, unique per tenant. |
| `available_for_calls` | boolean | Availability flag. |
| `teams` | array | `{name, is_manager}` for each team the user belongs to. |
| `can_login` | boolean | Whether the user has a console password. |
| `created_at` | timestamp | When the user was created. |

### GET /v1/users

List the tenant's users.

### POST /v1/users

Create a user with a console login.

| Field | Required | Description |
|---|---|---|
| `email` | yes | Lowercased and trimmed. Unique across tenants. |
| `password` | yes | At least 8 characters. |
| `role` | yes | `admin`, `supervisor` or `agent`. |
| `display_name` | no | |
| `sip_username` | no | Defaults to the email. Letters, digits and `. _ @ + -`, up to 64 characters. |

Returns the user object. `409` if the email or `sip_username` is taken; `400` on invalid input.

### PATCH /v1/users/{id}

Change any of `display_name`, `email`, `role` and `password`. Giving an agent created with `POST /v1/agents` an `email` and `password` gives them a login. Setting `email` to `""` removes the login. You cannot change your own role, remove your own login, or set a password while clearing the login. `404` for an unknown user, `409` if the email is taken.

### DELETE /v1/users/{id}

Delete a user. `204` on success; you cannot delete yourself.

## The logged-in user

Available to any console session, whatever its role. Each endpoint acts only on the caller's own user.

| Endpoint | What it does |
|---|---|
| `GET /v1/me` | The caller's profile: `id`, `display_name`, `email`, `role`, `sip_username`, `teams`. |
| `POST /v1/me/token` | Mints the caller's own agent token (same response as `/v1/agents/{id}/token`). |
| `POST /v1/me/availability` | `{"available": false}` sets the caller's Away flag. |
| `POST /v1/me/password` | `{"current": "...", "new": "..."}`. `401` if `current` is wrong; `new` must be at least 8 characters. |
| `GET /v1/me/stats?range=today\|7d` | `taken`, `missed`, `talk_seconds`, `avg_talk_seconds`. Missed means the call rang this user and they did not answer it, even if a teammate did. |
| `GET /v1/me/calls` | The caller's own calls, paginated. |
| `GET /v1/me/calls/{id}/recording` | Recording of one of the caller's own calls (`404` otherwise). |

## Team views

Available to any console session; results are limited to the caller's teams, or every team for admins and supervisors.

| Endpoint | What it does |
|---|---|
| `GET /v1/my-teams` | Teams the caller can see, with `is_manager`, queue depth and members. |
| `GET /v1/my-teams/{name}` | One team with members, stats and today's calls. `404` if not visible. |
| `GET /v1/my-teams/{name}/calls/{id}/recording` | A recording for a call belonging to the team. |
| `POST /v1/my-teams/{name}/members` | `{"user_id": "..."}`. Managers of the team, supervisors and admins. |
| `DELETE /v1/my-teams/{name}/members/{userID}` | Remove a member. Only a supervisor or admin can remove a manager (`403`). |
| `PUT /v1/my-teams/{name}/members/{userID}/manager` | `{"is_manager": true}`. Supervisors and admins only. |
