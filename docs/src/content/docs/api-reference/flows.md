---
title: Flows
description: Create and edit IVR flows, the flow graph JSON (nodes, edges and every node type), and the batch and interactive flow simulators.
---

A **flow** is an IVR: a graph of nodes (play a prompt, take a menu choice, call your backend, transfer to agents) joined by edges. Calls enter a flow from an [inbound number](/tring/api-reference/numbers/) with a `flow_id`, or from a web call. The console's flow editor writes the same JSON you can send here. For the concepts, see [IVR flows](/tring/features/ivr-flows/).

All routes on this page take a tenant API key or a console session. See [Authentication](/tring/api-reference/authentication/).

## The flow object

| Field | Type | Description |
|---|---|---|
| `id` | UUID | Flow ID. |
| `tenant_id` | UUID | Owning tenant. |
| `name` | string | Display name. Also used as the call log's "To" for web calls. Not required to be unique. |
| `description` | string | Free text. |
| `is_active` | boolean | Only active flows run on live calls. An inactive flow can still be simulated. |
| `is_outgoing_end` | boolean | Stored and returned. Not used by the call path today. |
| `definition` | object | The flow graph. See [Flow graph](#flow-graph). |
| `welcome_audio_url` | string | Stored and returned. Not used by the call path today. Put prompts on nodes instead. |
| `created_at` | timestamp | |
| `updated_at` | timestamp | Updated on every `PATCH`. |

## Flow graph

`definition` is a JSON object:

```json
{
  "version": 2,
  "entry_node": "n_start",
  "nodes": [ ... ],
  "edges": [ ... ]
}
```

| Field | Type | Description |
|---|---|---|
| `version` | integer | `2`. |
| `entry_node` | string | ID of the first node. Required: a flow with an empty `entry_node` can't run or be simulated. |
| `nodes` | array | The nodes. |
| `edges` | array | The connections. |

**Node**

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique within the flow. |
| `type` | string | One of the node types below. |
| `label` | string | Shown in the editor, and as the interactive simulator's prompt when a node has no text or audio. |
| `position` | `{x, y}` | Editor canvas position. Ignored at call time. |
| `config` | object | Type-specific settings. |

**Edge**

| Field | Type | Description |
|---|---|---|
| `from` | string | Source node ID. |
| `to` | string | Destination node ID. |
| `condition` | string | The outcome of `from` that follows this edge, for example `default`, `digit:1`, `timeout`. |

### How a call walks the graph

1. Start at `entry_node`.
2. Run the node. It produces an **outcome**.
3. Follow the edge from that node whose `condition` equals the outcome.
4. Stop when a node is terminal (`hangup`, `goto_flow`), when no edge matches the outcome, or after 64 nodes (so a loop can't run forever).

:::caution
On a live call, an outcome with no matching edge ends the flow. The simulators are more forgiving: they fall back to the node's `default` edge if there is one. Wire every outcome you care about explicitly, so the simulator and real calls agree.
:::

### Variables

Flows carry string variables you can use as `{{name}}` in templated fields. A call starts with:

| Variable | Set on | Value |
|---|---|---|
| `caller` | every call | Phone calls: the caller's number. Web calls: the `client_ref` your backend minted the visitor token with. |
| `did` | phone calls | The dialed number. |

`gather` nodes add the digits they collect (under `store_as`), and `http_callback` nodes add the top-level fields of a 2xx JSON response. An unknown `{{token}}` is left in the text as is.

### Prompts and text-to-speech

`greeting`, `menu`, `gather` and `hangup` nodes play `config.audio_file`: a `file_path` from the [audio library](/tring/api-reference/audio/). When text-to-speech is configured on the server and a node has `greeting_text` but no uploaded audio, Tring synthesises the text **when you save the flow** and writes the generated file's path into `audio_file`. Then:

- An uploaded `audio_file` always wins over `greeting_text`.
- Text containing `{{…}}` is not synthesised. There's no call-time TTS, so such a node plays nothing.
- Clearing the text on a node whose audio was synthesised removes that audio.
- Without TTS, a node with only `greeting_text` is silent on a live call.

### Node types

#### `start`

A passthrough anchor for the editor. No config. Outcome: `default`.

#### `greeting`

Plays a prompt and continues.

| Config | Type | Description |
|---|---|---|
| `audio_file` | string | Audio to play. |
| `greeting_text` | string | Text to synthesise on save (see above). |

Outcome: `default`.

#### `menu`

Plays a prompt and waits for one digit. The caller can press a valid option while the prompt is still playing; invalid keys are ignored until the prompt ends. After the prompt, an invalid key replays the prompt.

| Config | Type | Default | Description |
|---|---|---|---|
| `audio_file` / `greeting_text` | string | | The prompt. |
| `options` | object | `{}` | Valid digits as keys, for example `{"1": {"label": "Sales"}, "2": {"label": "Support"}}`. Only the keys matter at call time. |
| `timeout_seconds` | integer | `10` | How long to wait for a digit after the prompt. |
| `max_retries` | integer | `3` | Invalid choices allowed before giving up. |

Outcomes: `digit:<d>` for a valid option (for example `digit:1`), `timeout` when no digit arrives, `max_retries` after too many invalid choices.

#### `gather`

Plays a prompt and collects several digits, for example an account number. Any key interrupts the prompt. The terminator key (`#` by default) ends input early.

| Config | Type | Default | Description |
|---|---|---|---|
| `audio_file` / `greeting_text` | string | | The prompt. |
| `min_digits` | integer | `1` | Fewer digits than this takes the `timeout` branch and stores nothing. Set it equal to `max_digits` to require an exact length. |
| `max_digits` | integer | `10` | Stop after this many digits. |
| `num_digits` | integer | | Older name for `max_digits`, used on live calls only when `max_digits` is not set. |
| `terminator` | string | `#` | Key that ends input early. It isn't stored. |
| `timeout_seconds` | integer | `10` | Wait per digit. A timeout after some digits ends input with what was collected. |
| `store_as` | string | | Variable to save the digits in. |

Outcomes: `default` once at least `min_digits` digits are collected, `timeout` if fewer are.

:::note
The editor's `max_retries` field on gather is not used at call time.
:::

#### `http_callback`

Calls your backend so the flow can branch on data only you have. The request runs from the media gateway.

| Config | Type | Default | Description |
|---|---|---|---|
| `url` | string | | Request URL. `{{vars}}` are substituted. |
| `method` | string | `GET` | HTTP method. |
| `headers` | object | `{}` | String headers to send, for example an auth header for your backend. |
| `body_template` | string | | Request body, with `{{vars}}` substituted. When set, `Content-Type: application/json` is sent. |
| `timeout_seconds` | integer | `10` | Request timeout. |

Outcomes: `http:2xx` on a 2xx response, `http:non2xx` on anything else, including a bad URL, a timeout or a network error. The call never fails because of the callback.

On a 2xx with a JSON object body (up to 1 MiB is read), each top-level field becomes a variable: strings as is, booleans as `true`/`false`, numbers in plain form, `null` as an empty string, objects and arrays as JSON text. A response `{"team": "priority", "vip": true}` sets `{{team}}` and `{{vip}}`.

:::caution
Use an `https://` URL, and authenticate the request with a secret header you check in your backend. Don't put secrets in the URL. Values you return become variables a later `transfer` can dial, so never return a number your backend hasn't validated.
:::

#### `transfer`

Rings agents and connects the caller, or dials a phone number.

| Config | Type | Description |
|---|---|---|
| `team` | string | Ring this team using its own strategy, ring time, queue wait and hold music. See [Teams](/tring/api-reference/teams/). |
| `agent_sip` | string | Ring this one agent (by `sip_username`). |
| `label` | string | What the agent's incoming-call screen shows instead of the caller reference. Use it to keep the caller's real number away from agents. |
| `number` | string | Dial this phone number instead of ringing agents. The presence of the `number` key switches the node to number mode. |
| `caller_id` | string | Number mode: the caller ID to present. Must be one of your tenant's numbers. Empty uses the trunk's own DID. |
| `ring_seconds` | integer | Number mode: how long to ring, default `30`. |

All of these accept `{{vars}}`. In agent mode, if both `agent_sip` and `team` are set, `agent_sip` is used. With neither, all available agents ring.

Outcomes:

- `completed`: an agent, or the dialed number, answered. In agent mode, the `completed` edge runs after the agent hangs up, if the caller is still on the line (for example, to play a survey or a goodbye). If the caller hangs up first, the flow ends.
- `no_answer`: nobody answered in time, no one was available, or the number couldn't be dialed. A number or `caller_id` that still contains an unresolved `{{var}}` is not dialed and gives `no_answer`.

:::danger
A `number` built from caller input or from an `http_callback` response is dialed on your trunk at your cost. Validate and allow-list it in your backend before returning it. Don't build numbers directly from a `gather`.
:::

#### `timing`

Branches on business hours.

| Config | Type | Description |
|---|---|---|
| `schedule` | array | One entry per weekday: `{"day": "monday", "enabled": true, "start_time": "09:00", "end_time": "17:00"}`. |

Outcomes: `in_hours` or `out_of_hours`.

- A day with `enabled: false` is closed all day.
- An enabled day is open from `start_time` (inclusive) to `end_time` (exclusive). If the times are missing or invalid, it's open all day.
- A day not in the schedule is closed. No `schedule` at all means always open.
- Times are evaluated in the gateway server's local time zone. There's no per-tenant time zone yet.

#### `goto_flow`

| Config | Type | Description |
|---|---|---|
| `flow_id` | string | The flow to jump to. |

Not implemented yet: on a live call this node ends the flow. The simulator records "would jump to flow …" and stops.

#### `hangup`

Plays an optional goodbye (`audio_file` / `greeting_text`) and ends the call. Terminal.

### Example definition

```json
{
  "version": 2,
  "entry_node": "n_start",
  "nodes": [
    { "id": "n_start", "type": "start", "label": "Start", "position": {"x": 0, "y": 0}, "config": {} },
    { "id": "n_welcome", "type": "greeting", "label": "Welcome", "position": {"x": 0, "y": 120},
      "config": { "greeting_text": "Thanks for calling Example Corp." } },
    { "id": "n_menu", "type": "menu", "label": "Main menu", "position": {"x": 0, "y": 240},
      "config": {
        "greeting_text": "Press 1 for sales. Press 2 to check an order.",
        "options": { "1": {"label": "Sales"}, "2": {"label": "Orders"} },
        "timeout_seconds": 10,
        "max_retries": 3
      } },
    { "id": "n_sales", "type": "transfer", "label": "Sales team", "position": {"x": -200, "y": 360},
      "config": { "team": "sales" } },
    { "id": "n_order", "type": "gather", "label": "Order number", "position": {"x": 200, "y": 360},
      "config": {
        "greeting_text": "Enter your six digit order number.",
        "num_digits": 6, "max_digits": 6, "timeout_seconds": 10, "store_as": "order"
      } },
    { "id": "n_bye", "type": "hangup", "label": "Goodbye", "position": {"x": 0, "y": 480},
      "config": { "greeting_text": "Goodbye." } }
  ],
  "edges": [
    { "from": "n_start",   "to": "n_welcome", "condition": "default" },
    { "from": "n_welcome", "to": "n_menu",    "condition": "default" },
    { "from": "n_menu",    "to": "n_sales",   "condition": "digit:1" },
    { "from": "n_menu",    "to": "n_order",   "condition": "digit:2" },
    { "from": "n_menu",    "to": "n_bye",     "condition": "timeout" },
    { "from": "n_menu",    "to": "n_bye",     "condition": "max_retries" },
    { "from": "n_sales",   "to": "n_bye",     "condition": "no_answer" },
    { "from": "n_order",   "to": "n_bye",     "condition": "default" },
    { "from": "n_order",   "to": "n_bye",     "condition": "timeout" }
  ]
}
```

### POST /v1/flows

Create a flow.

**Auth:** tenant API key or console session.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Display name. |
| `description` | string | no | Default `""`. |
| `is_active` | boolean | no | Default `true`. |
| `is_outgoing_end` | boolean | no | Default `false`. |
| `definition` | object | no | The flow graph. Default: an empty graph with `entry_node: ""`, which you must fill in before it can run. |
| `welcome_audio_url` | string | no | Default `""`. |

`definition` is stored as sent, apart from the TTS pre-rendering above. It is **not** validated on save: a missing node or a typo in a `condition` only shows up when the flow runs. Run the [simulator](#post-v1flowsidsimulate) after every change.

```bash
curl -sS -X POST "$TRING_URL/v1/flows" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d @- <<'EOF'
{
  "name": "Main line",
  "description": "Sales and order status",
  "definition": { "version": 2, "entry_node": "n_start", "nodes": [], "edges": [] }
}
EOF
```

**Response:** `201 Created` with the flow object.

```json
{
  "id": "5c1a7b22-2222-4e3f-8b00-000000000001",
  "tenant_id": "7d3e9f10-0000-4000-8000-000000000001",
  "name": "Main line",
  "description": "Sales and order status",
  "is_active": true,
  "is_outgoing_end": false,
  "definition": { "version": 2, "entry_node": "n_start", "nodes": [], "edges": [] },
  "welcome_audio_url": "",
  "created_at": "2026-10-16T10:15:00.401233+05:30",
  "updated_at": "2026-10-16T10:15:00.401233+05:30"
}
```

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid JSON body` |
| `400` | `name is required` |
| `500` | `insert failed: …` |

### GET /v1/flows

List your tenant's flows, most recently updated first. Each item includes its full `definition`.

**Auth:** tenant API key or console session.

**Query parameters**

| Param | Description |
|---|---|
| `page` | 1-based page number. Without it, every flow is returned in one envelope. |
| `limit` | Page size when `page` is set. Default `25`, max `200`. |

```bash
curl -sS "$TRING_URL/v1/flows?page=1&limit=25" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `200 OK` with the [pagination envelope](/tring/api-reference/overview/#pagination). Without `page`, `limit` equals the number of items and `offset` is `0`.

```json
{
  "items": [
    {
      "id": "5c1a7b22-2222-4e3f-8b00-000000000001",
      "tenant_id": "7d3e9f10-0000-4000-8000-000000000001",
      "name": "Main line",
      "description": "Sales and order status",
      "is_active": true,
      "is_outgoing_end": false,
      "definition": { "version": 2, "entry_node": "n_start", "nodes": [], "edges": [] },
      "welcome_audio_url": "",
      "created_at": "2026-10-16T10:15:00.401233+05:30",
      "updated_at": "2026-10-16T10:15:00.401233+05:30"
    }
  ],
  "total": 1,
  "limit": 25,
  "offset": 0
}
```

**Errors**

| Status | Message |
|---|---|
| `500` | `query failed` / `scan failed` / `count failed` / `encode failed` |

### GET /v1/flows/{id}

Fetch one flow.

**Auth:** tenant API key or console session.

```bash
curl -sS "$TRING_URL/v1/flows/5c1a7b22-2222-4e3f-8b00-000000000001" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `200 OK` with the flow object.

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid id` |
| `404` | `flow not found` |
| `500` | `query failed` |

### PATCH /v1/flows/{id}

Update a flow. Leave out any field to keep it. Calls that start after the update use the new version; calls already in progress keep the version they started with.

**Auth:** tenant API key or console session.

**Request body.** All fields optional.

| Field | Type | Description |
|---|---|---|
| `name` | string | |
| `description` | string | |
| `is_active` | boolean | `false` stops the flow running on new calls. |
| `is_outgoing_end` | boolean | |
| `definition` | object | Replaces the whole graph. Send the complete definition, not a diff. |
| `welcome_audio_url` | string | |

```bash
curl -sS -X PATCH "$TRING_URL/v1/flows/5c1a7b22-2222-4e3f-8b00-000000000001" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"is_active": false}'
```

**Response:** `200 OK` with the updated flow object.

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid id` |
| `400` | `invalid JSON body` |
| `404` | `flow not found` |
| `500` | `update failed: …` |

### DELETE /v1/flows/{id}

Delete a flow. Numbers that pointed at it keep existing with `flow_id` set to `null`, so inbound calls to them are rejected until you attach another flow.

**Auth:** tenant API key or console session.

```bash
curl -sS -X DELETE "$TRING_URL/v1/flows/5c1a7b22-2222-4e3f-8b00-000000000001" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `204 No Content`.

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid id` |
| `404` | `flow not found` |
| `500` | `delete failed` |

## Simulator

The simulator walks a saved flow without placing a call: nothing plays, no one rings and no HTTP request is made. It's what the console's **Test** panel uses. There are two modes:

- **Batch** (`POST /v1/flows/{id}/simulate`): you give all the inputs up front and get the full trace back in one response.
- **Interactive** (`/simulate/start`, then `/input`): the walk pauses at each `menu` and `gather` and waits for you to send digits.

Both simulate the flow as **saved**. `PATCH` your changes first.

### Trace steps

Both modes return `steps`, one per node visited:

| Field | Type | Description |
|---|---|---|
| `node_id` | string | |
| `node_type` | string | |
| `action` | string | `passthrough`, `play_audio`, `speak`, `prompt_dtmf`, `http_call`, `transfer`, `branch`, `hangup` or `unknown`. |
| `detail` | string | What the node would do: the audio file or text, `GET https://…`, `team sales`, `would jump to flow …`. Omitted when empty. |
| `dtmf_input` | string | Digits consumed at a menu or gather. Omitted when none. |
| `outcome` | string | The outcome taken (`default`, `digit:1`, `timeout`, …). Empty for a terminal node. |

Live calls record the same step shape in a call's [`ivr_path`](/tring/api-reference/calls/#the-call-object).

### POST /v1/flows/{id}/simulate

Run a batch simulation.

**Auth:** tenant API key or console session.

**Request body.** Optional. An empty body runs with the defaults.

| Field | Type | Required | Description |
|---|---|---|---|
| `caller` | string | no | Simulated caller. Default `+10000000000`. Not added to `vars`. |
| `dtmf` | array of strings | no | Digits in order. Each `menu` takes one entry; each `gather` takes entries until `max_digits` is reached. When the list runs out, a menu or gather times out. |
| `vars` | object | no | Starting variables (string values), so you can test a branch without walking the gather that sets them. |
| `now` | timestamp | no | Clock for `timing` nodes, RFC 3339, for example `2026-10-17T20:30:00+05:30`. Default: the server's current time. |
| `transfer_outcome` | string | no | Force every transfer's outcome: `no_answer` or `queue_timeout`. Anything else (or empty) is `completed`. |
| `http_outcome` | string | no | `http:non2xx` forces every callback down its failure branch. Default `http:2xx`. |

Differences from a live call:

- A menu digit that isn't an option goes straight to `max_retries`. There's no retry loop.
- A `gather` reads `max_digits` only (not `num_digits`) and ignores `terminator`.
- An outcome with no matching edge falls back to the node's `default` edge.
- `queue_timeout` is a simulator-only outcome. On a live call an unanswered transfer is `no_answer`.

```bash
curl -sS -X POST "$TRING_URL/v1/flows/5c1a7b22-2222-4e3f-8b00-000000000001/simulate" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"dtmf": ["1"], "transfer_outcome": "no_answer"}'
```

**Response:** `200 OK`. With the [example definition](#example-definition) above (and TTS off, so prompts show as text):

```json
{
  "steps": [
    { "node_id": "n_start", "node_type": "start", "action": "passthrough", "outcome": "default" },
    { "node_id": "n_welcome", "node_type": "greeting", "action": "speak",
      "detail": "Thanks for calling Example Corp.", "outcome": "default" },
    { "node_id": "n_menu", "node_type": "menu", "action": "prompt_dtmf",
      "detail": "Press 1 for sales. Press 2 to check an order.", "dtmf_input": "1", "outcome": "digit:1" },
    { "node_id": "n_sales", "node_type": "transfer", "action": "transfer",
      "detail": "team sales", "outcome": "no_answer" },
    { "node_id": "n_bye", "node_type": "hangup", "action": "hangup", "outcome": "" }
  ],
  "ended_at": "n_bye",
  "hangup_cause": "NORMAL_CLEARING",
  "vars": {}
}
```

| Field | Description |
|---|---|
| `steps` | The trace. |
| `ended_at` | ID of the last node. |
| `hangup_cause` | `NORMAL_CLEARING` (ended on a `hangup` node), `END_OF_FLOW` (ended on another terminal node, such as `goto_flow`) or `TEMPORARY_FAILURE` (a node or edge was missing). Empty when the walk hit the depth limit. |
| `vars` | Variables at the end, including `vars` you passed and `gather` results. |
| `error` | Set when the walk stopped early: `node "…" not found`, `node … has no edge for outcome "…"` or `exceeded max walk depth (64)`. The partial `steps` are still returned. |

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid id` |
| `400` | `invalid JSON body` |
| `404` | `flow not found` |
| `422` | `parse flow: …` or `flow has no entry node` |
| `500` | `query failed` |

### POST /v1/flows/{id}/simulate/start

Start an interactive session. The walk runs until the first `menu` or `gather` (and pauses there) or until the flow ends.

**Auth:** tenant API key or console session.

**Request body.** Optional. Takes `caller`, `vars` and `now` as in the batch simulator. `dtmf`, `transfer_outcome` and `http_outcome` are ignored: in interactive mode every transfer is `completed` and every callback is `http:2xx`.

```bash
curl -sS -X POST "$TRING_URL/v1/flows/5c1a7b22-2222-4e3f-8b00-000000000001/simulate/start" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `200 OK` with a session snapshot (see the [worked example](#worked-example-an-interactive-session) below).

| Field | Type | Description |
|---|---|---|
| `session_id` | UUID | Pass it to `/input`. |
| `status` | string | `awaiting_dtmf`, `ended` or `error`. |
| `steps` | array | Every step since the session started. |
| `vars` | object | Current variables. |
| `awaiting_node` | object | When `status` is `awaiting_dtmf`: `node_id`, `node_type` and `prompt` (the node's `greeting_text`, else `play: <audio_file>`, else its `label`). |
| `ended_at` | string | When ended: the last node's ID. |
| `hangup_cause` | string | When ended: as in the batch simulator. |
| `error` | string | When `status` is `error`. |

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid id` |
| `400` | `invalid JSON body` |
| `404` | `flow not found` |
| `422` | `parse flow: …` or `flow has no entry node` |
| `500` | `query failed` |

### POST /v1/flows/simulate/sessions/{sid}/input

Send digits to a paused session. The digits are consumed by the paused node, then the walk continues to the next pause or the end.

**Auth:** tenant API key or console session.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `dtmf` | string | yes | For a `menu`, one digit. For a `gather`, the whole entry (for example `"123456"`). An empty string leaves the session paused where it is; it doesn't simulate a timeout. |

A menu digit that isn't an option takes the `max_retries` outcome. Sending input to a session that has already ended returns its final snapshot unchanged.

```bash
curl -sS -X POST "$TRING_URL/v1/flows/simulate/sessions/8f0e4d2c-5555-4b00-9e00-000000000001/input" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"dtmf": "2"}'
```

**Response:** `200 OK` with the updated session snapshot.

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid JSON body` |
| `404` | `invalid session id` or `session not found`. Sessions expire after 10 minutes idle, and only the tenant that started a session can drive it. Start a new one. |

:::note
Sessions live in the memory of the API process that started them. They're lost on restart, and if you run more than one API replica, send a session's requests to the same replica.
:::

### Worked example: an interactive session

Using the [example definition](#example-definition), with TTS off.

**1. Start.** The walk passes `start` and `greeting`, then pauses at the menu.

```bash
curl -sS -X POST "$TRING_URL/v1/flows/5c1a7b22-2222-4e3f-8b00-000000000001/simulate/start" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

```json
{
  "session_id": "8f0e4d2c-5555-4b00-9e00-000000000001",
  "status": "awaiting_dtmf",
  "steps": [
    { "node_id": "n_start", "node_type": "start", "action": "passthrough", "outcome": "default" },
    { "node_id": "n_welcome", "node_type": "greeting", "action": "speak",
      "detail": "Thanks for calling Example Corp.", "outcome": "default" }
  ],
  "vars": {},
  "awaiting_node": {
    "node_id": "n_menu",
    "node_type": "menu",
    "prompt": "Press 1 for sales. Press 2 to check an order."
  }
}
```

**2. Press 2.** The menu takes `digit:2`, and the walk pauses again at the gather.

```bash
curl -sS -X POST "$TRING_URL/v1/flows/simulate/sessions/8f0e4d2c-5555-4b00-9e00-000000000001/input" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"dtmf": "2"}'
```

```json
{
  "session_id": "8f0e4d2c-5555-4b00-9e00-000000000001",
  "status": "awaiting_dtmf",
  "steps": [
    { "node_id": "n_start", "node_type": "start", "action": "passthrough", "outcome": "default" },
    { "node_id": "n_welcome", "node_type": "greeting", "action": "speak",
      "detail": "Thanks for calling Example Corp.", "outcome": "default" },
    { "node_id": "n_menu", "node_type": "menu", "action": "prompt_dtmf",
      "detail": "Press 1 for sales. Press 2 to check an order.", "dtmf_input": "2", "outcome": "digit:2" }
  ],
  "vars": {},
  "awaiting_node": {
    "node_id": "n_order",
    "node_type": "gather",
    "prompt": "Enter your six digit order number."
  }
}
```

**3. Enter the order number.** The gather stores it in `order`, and the flow runs to the hangup node.

```bash
curl -sS -X POST "$TRING_URL/v1/flows/simulate/sessions/8f0e4d2c-5555-4b00-9e00-000000000001/input" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"dtmf": "123456"}'
```

```json
{
  "session_id": "8f0e4d2c-5555-4b00-9e00-000000000001",
  "status": "ended",
  "steps": [
    { "node_id": "n_start", "node_type": "start", "action": "passthrough", "outcome": "default" },
    { "node_id": "n_welcome", "node_type": "greeting", "action": "speak",
      "detail": "Thanks for calling Example Corp.", "outcome": "default" },
    { "node_id": "n_menu", "node_type": "menu", "action": "prompt_dtmf",
      "detail": "Press 1 for sales. Press 2 to check an order.", "dtmf_input": "2", "outcome": "digit:2" },
    { "node_id": "n_order", "node_type": "gather", "action": "prompt_dtmf",
      "detail": "Enter your six digit order number.", "dtmf_input": "123456", "outcome": "default" },
    { "node_id": "n_bye", "node_type": "hangup", "action": "hangup", "outcome": "" }
  ],
  "vars": { "order": "123456" },
  "ended_at": "n_bye",
  "hangup_cause": "NORMAL_CLEARING"
}
```
