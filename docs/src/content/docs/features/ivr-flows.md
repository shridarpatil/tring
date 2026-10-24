---
title: IVR flows
description: The flow graph model, every node type and its fields, flow variables, transfer targets, text-to-speech prompts and the Test panel.
---

An IVR flow is a graph of nodes that decides what a caller hears and where the call goes. You build flows in the console's visual editor (**Flows**) or through the [Flows API](/tring/api-reference/flows/). The same flow runs for web calls, phone calls and WhatsApp calls.

A flow runs when:

- a visitor web call starts (the flow is pinned in the visitor's call token, see [Web calling](/tring/features/web-calling/)), or
- a phone or WhatsApp call arrives on an inbound number that has a flow attached (see [Numbers and trunks](/tring/features/numbers-and-trunks/)).

## The flow model

A flow record has a name, an `is_active` flag and a `definition`. Only **active** flows can run or be used as a default flow.

The definition is JSON:

```json
{
  "version": 2,
  "entry_node": "node_start",
  "nodes": [
    { "id": "node_start", "type": "start", "label": "Start", "position": { "x": 0, "y": 0 }, "config": {} }
  ],
  "edges": [
    { "from": "node_start", "to": "welcome", "condition": "default" }
  ]
}
```

| Field | What it is |
|---|---|
| `version` | `2`. The editor loads only version 2 definitions. |
| `entry_node` | Id of the first node. The editor always saves its Start node, `node_start`. Required. |
| `nodes[]` | `id`, `type`, `label`, `position` (editor canvas only) and a type-specific `config` object. |
| `edges[]` | `from` node id, `to` node id, and `condition`: the **outcome** of the `from` node that follows this edge. |

### How a call walks the graph

1. Start at `entry_node`.
2. Run the node. It returns an outcome, for example `default`, `digit:1` or `no_answer`.
3. Follow the edge whose `from` is this node and whose `condition` equals the outcome.
4. Stop when a node is terminal (Hangup), when **no edge matches the outcome**, or after 64 steps (protects against loops).

Wire every outcome you care about. On a live call, an outcome with no edge ends the flow. The path a call took is saved on the call record as `ivr_path` (see [Recording and call logs](/tring/features/recording-and-call-logs/)).

## Node types

Prompt fields are shared by several nodes:

| Field | What it does |
|---|---|
| `greeting_text` | Text to speak. Synthesized to audio when you save the flow (see [Text-to-speech](#text-to-speech-prompts)). |
| `audio_file` | A sound from your [sound library](/tring/features/sounds-and-hold-music/) (its `file_path`). An uploaded sound always wins over `greeting_text`. |

### Start

`type: "start"`. The entry anchor. No config. Outcome: `default`.

### Greeting

`type: "greeting"`. Plays a prompt, then continues.

| Field | Default | What it does |
|---|---|---|
| `greeting_text` / `audio_file` | | The prompt. |
| `interruptible` | `false` | When `true`, the caller's first keypress stops the greeting and the flow moves on. That key only skips the greeting; it isn't passed to a following menu. |

Outcome: `default`.

### Menu

`type: "menu"`. Plays a prompt and collects **one** digit.

| Field | Default | What it does |
|---|---|---|
| `greeting_text` / `audio_file` | | The prompt, for example "Press 1 for sales, 2 for support". |
| `options` | `{}` | Map of digit to option, for example `{"1": {"label": "Sales"}}`. Only these digits are valid. |
| `timeout_seconds` | `10` | How long to wait for a digit after the prompt ends. |
| `max_retries` | `3` | How many times to replay the prompt after an invalid digit. |

The caller can press a valid option while the prompt is still playing (barge-in); invalid keys are ignored until the prompt ends.

| Outcome | When |
|---|---|
| `digit:<d>` | A configured digit was pressed, for example `digit:1`. |
| `timeout` | No digit within `timeout_seconds`. |
| `max_retries` | Invalid digits on every attempt. |

### Gather

`type: "gather"`. Plays a prompt and collects several digits into a variable.

| Field | Default | What it does |
|---|---|---|
| `greeting_text` / `audio_file` | | The prompt, for example "Enter your account number followed by #". |
| `store_as` | | Variable name to store the digits in, for example `account_number`. |
| `timeout_seconds` | `10` | Wait for each digit. |
| `num_digits` | `1` | Maximum digits the live call collects. |
| `max_digits`, `terminator`, `max_retries` | `10`, `#`, `3` | Saved by the editor and used by the Test panel (`max_digits`). |

Input ends at `#`, at the digit limit, or on timeout. Any key barges in on the prompt.

| Outcome | When |
|---|---|
| `default` | At least one digit was collected (stored in `store_as`). |
| `timeout` | Nothing was entered. |

:::caution
The live call reads the digit limit from `num_digits` (default `1`) and always ends input on `#`. The editor saves `max_digits` and `terminator` instead, so a Gather built in the editor collects a single digit on a real call. Until the two agree, add `"num_digits": <n>` to the node's config through the [Flows API](/tring/api-reference/flows/) when you need more than one digit.
:::

### HTTP callback

`type: "http_callback"`. Calls your backend so the flow can route on data only you have.

| Field | Default | What it does |
|---|---|---|
| `url` | | Request URL. Variables are substituted. |
| `method` | `GET` | `GET`, `POST`, `PUT`, `PATCH` or `DELETE`. |
| `headers` | `{}` | Map of header name to value (string values only). |
| `body_template` | | Request body. Variables are substituted. When set, `Content-Type: application/json` is sent. |
| `timeout_seconds` | `10` | Request timeout. |

If the response is `2xx` with a JSON **object** body, each top-level field becomes a flow variable (strings as-is, numbers and booleans as text, nested values as JSON). Up to 1 MiB of the response is read. The callback never fails the call: a bad URL, a network error or a non-`2xx` status simply takes the error branch.

| Outcome | When |
|---|---|
| `http:2xx` | The response status was 200–299. |
| `http:non2xx` | Any other status, a timeout, or a request error. |

:::caution[Security]
Use an `https://` URL. Authenticate the request with a header (for example `Authorization: Bearer <secret>`) and check it on your side, because anyone who learns the URL can call it. Header values are stored in the flow definition in plain text, so use a dedicated, low-privilege secret, never a Tring API key.

The URL must resolve to a public address. Requests to loopback, private, link-local (including cloud metadata) and other internal ranges are refused, on redirects too (at most 3 are followed), and take `http:non2xx`. A backend on a private network needs its range in [`TRING_OUTBOUND_ALLOW_CIDRS`](/tring/getting-started/configuration/).
:::

### Transfer

`type: "transfer"`. Hands the call to people. The editor's **Transfer to** picker sets one of these targets:

| Target | Config | What happens |
|---|---|---|
| All available agents | none of `team`, `agent_sip`, `number` | Rings every connected, free agent of the tenant at once. |
| A team | `team`: team name | Rings the team's members with the team's strategy, queueing if all are busy. See [Agents and routing](/tring/features/agents-and-routing/). |
| One agent | `agent_sip`: the agent's `sip_username` | Rings that agent. |
| A phone number | `number`: the number to dial | Dials the number over your outbound trunk and connects it to the caller. |

All target fields accept variables, for example `"team": "{{team}}"` or `"number": "{{dest}}"` from an earlier HTTP callback.

Other fields:

| Field | Applies to | Default | What it does |
|---|---|---|---|
| `label` | team, agent, all | the caller's identity | What the ringing agent sees instead of the caller's number. Use it to mask the number, for example `Customer {{mask}}`. With no label and no caller identity, agents see `Web Caller`. |
| `ring_seconds` | number | `30` | How long to ring the number. |
| `caller_id` | number | the trunk's own number | The number the person dialed sees. Must be one of your tenant's active numbers, or the dial is refused. |
| `greeting_text` | all | | An announcement played to the caller before anyone rings, for example "This call may be recorded for quality purposes". Spoken with text-to-speech when the flow is saved. |
| `audio_file` | all | | An announcement from your sound library instead of text. If it can't be played, the transfer goes ahead without it. |
| `priority` | team | `false` | Saved by the editor ("let them skip the line"). The live routing engine does not apply it yet; callers queue in arrival order. |

While a number rings, the caller hears the tenant's hold music, which stops when it answers. A number transfer needs the SIP plug (`TRING_FSPLUG_ENABLED=true`) and an outbound trunk. The live call normalizes the number as an Indian number (`+91XXXXXXXXXX`). A blank number, or one that still contains an unresolved variable, is not dialed.

| Outcome | When |
|---|---|
| `completed` | Someone answered. The edge runs **after the agent (or the dialed number) hangs up** while the caller, web or phone, is still on the line, so you can play a survey or goodbye. If the caller hangs up first, the flow stops. With no `completed` edge, the call ends when the agent or dialed number hangs up. |
| `no_answer` | Nobody answered in time, no one was available, the team wait ran out, the number was busy or invalid, there was no trunk, or the caller ID was refused. |

The editor also shows a `queue_timeout` handle and the Test panel can force it, but live calls report a queue wait that runs out as `no_answer`. Wire `no_answer` for your fallback.

### Timing

`type: "timing"`. Branches on business hours.

| Field | What it does |
|---|---|
| `schedule` | One entry per weekday: `{"day": "monday", "enabled": true, "start_time": "09:00", "end_time": "17:00"}`. |

Hours are evaluated in the server's timezone (`TZ`, see [Configuration](/tring/getting-started/configuration/#0-timezone)). A day with `enabled: false`, or a day missing from the schedule, is closed. An enabled day with missing or invalid times is open all day. A node with no schedule is always open. The end time is exclusive.

| Outcome | When |
|---|---|
| `in_hours` | Open now. |
| `out_of_hours` | Closed now. |

### Go to flow

`type: "goto_flow"`. Config: `flow_id`, the target flow.

:::note[Not implemented yet]
On a live call, Go to flow ends the flow; it does not jump. The Test panel stops at this node with "would jump to flow …". Build the target steps into the same flow for now.
:::

### Hangup

`type: "hangup"`. Plays an optional prompt (`greeting_text` / `audio_file`), then ends the call. Terminal: no outgoing edges.

## Variables

Use `{{name}}` in an HTTP callback's `url` and `body_template`, and in a Transfer's `team`, `agent_sip`, `number`, `caller_id` and `label`. Unknown variables are left in place as `{{name}}`, which makes typos visible in logs.

| Variable | Set by | Value |
|---|---|---|
| `caller` | the call | Phone and WhatsApp: the caller's number (a WhatsApp caller without a visible number can appear as a WhatsApp user id). Web: the visitor's `client_ref`, empty for anonymous visitors. |
| `did` | the call | Phone and WhatsApp: the number the caller dialed. Not set on web calls. |
| your `store_as` name | Gather | The digits entered. |
| any top-level field | HTTP callback | Fields of a `2xx` JSON object response. |

Example: route callers by your CRM's answer.

```json
{
  "id": "lookup",
  "type": "http_callback",
  "config": {
    "method": "POST",
    "url": "https://crm.example.com/tring/route",
    "headers": { "Authorization": "Bearer <ivr-callback-secret>" },
    "body_template": "{\"caller\":\"{{caller}}\",\"did\":\"{{did}}\"}",
    "timeout_seconds": 5
  }
}
```

If your backend answers `{"team": "priority", "mask": "A-1042"}`, a following Transfer with `"team": "{{team}}"` and `"label": "Customer {{mask}}"` rings the `priority` team and shows `Customer A-1042`.

Text with variables is not synthesized to speech (it can only be known at call time), so keep spoken prompts static.

## Text-to-speech prompts

When you save a flow, the api synthesizes every node's `greeting_text` that has no uploaded `audio_file`, using local Piper and ffmpeg, into a cached Opus file under `<audio_dir>/tts`. It writes that file's path into the node's `audio_file`, so calls play it like any other sound, with no speech synthesis during the call.

- Edit the text and save: the prompt is re-synthesized. Clear the text: the synthesized audio is removed.
- An uploaded sound in `audio_file` is never replaced.
- Text containing `{{` is skipped.
- If TTS is unavailable (no Piper, model or ffmpeg), the save still succeeds and text-only nodes play nothing.

See [Sounds and hold music](/tring/features/sounds-and-hold-music/#text-to-speech) for voices.

## Test panel (simulator)

The editor's **Test** panel walks the saved flow without placing a call. Nothing is played, dialed or requested.

1. Enter a caller number (optional; defaults to `+10000000000`) and start.
2. The walk pauses at each Menu and Gather and shows the prompt. Type digits to continue.
3. The trace shows each node, its action (`play_audio`, `speak`, `prompt_dtmf`, `http_call`, `transfer`, `branch`, `hangup`), what was entered and the outcome taken.

Simulator behaviour to keep in mind:

- HTTP callbacks are not sent; they take `http:2xx` unless forced to `http:non2xx`.
- Transfers take `completed` unless forced to `no_answer` or `queue_timeout`.
- Timing nodes use the current time unless a time is supplied.
- An invalid menu digit goes straight to `max_retries`.
- When an outcome has no edge, the simulator falls back to the node's `default` edge if there is one. A live call does not; it ends. Wire outcomes explicitly.

The same simulator is available over the API as a one-shot run (`POST /v1/flows/{id}/simulate` with `caller`, `dtmf[]`, `vars`, `now`, `transfer_outcome`, `http_outcome`) and as an interactive session (`POST /v1/flows/{id}/simulate/start`, then `POST /v1/flows/simulate/sessions/{sid}/input` with `{"dtmf": "1"}`). Idle sessions expire after 10 minutes. See the [Flows API](/tring/api-reference/flows/).

## Full example

Open hours: a menu. Press 1 to look the caller up in your CRM and ring the team it names (falling back to `support`); press 2 to ring an on-call phone. Closed hours: a message and hang up.

```json
{
  "version": 2,
  "entry_node": "node_start",
  "nodes": [
    { "id": "node_start", "type": "start", "label": "Start", "position": { "x": 0, "y": 0 }, "config": {} },
    {
      "id": "hours", "type": "timing", "label": "Business hours", "position": { "x": 200, "y": 0 },
      "config": {
        "schedule": [
          { "day": "monday",    "enabled": true,  "start_time": "09:00", "end_time": "18:00" },
          { "day": "tuesday",   "enabled": true,  "start_time": "09:00", "end_time": "18:00" },
          { "day": "wednesday", "enabled": true,  "start_time": "09:00", "end_time": "18:00" },
          { "day": "thursday",  "enabled": true,  "start_time": "09:00", "end_time": "18:00" },
          { "day": "friday",    "enabled": true,  "start_time": "09:00", "end_time": "18:00" },
          { "day": "saturday",  "enabled": false, "start_time": "09:00", "end_time": "18:00" },
          { "day": "sunday",    "enabled": false, "start_time": "09:00", "end_time": "18:00" }
        ]
      }
    },
    {
      "id": "main_menu", "type": "menu", "label": "Main menu", "position": { "x": 400, "y": 0 },
      "config": {
        "greeting_text": "Thanks for calling. Press 1 for support, or 2 for urgent issues.",
        "timeout_seconds": 8,
        "max_retries": 2,
        "options": { "1": { "label": "Support" }, "2": { "label": "Urgent" } }
      }
    },
    {
      "id": "lookup", "type": "http_callback", "label": "CRM lookup", "position": { "x": 600, "y": -100 },
      "config": {
        "method": "POST",
        "url": "https://crm.example.com/tring/route",
        "headers": { "Authorization": "Bearer <ivr-callback-secret>" },
        "body_template": "{\"caller\":\"{{caller}}\",\"did\":\"{{did}}\"}",
        "timeout_seconds": 5
      }
    },
    {
      "id": "to_crm_team", "type": "transfer", "label": "CRM team", "position": { "x": 800, "y": -150 },
      "config": { "team": "{{team}}", "label": "Customer {{mask}}" }
    },
    {
      "id": "to_support", "type": "transfer", "label": "Support team", "position": { "x": 800, "y": -50 },
      "config": { "team": "support" }
    },
    {
      "id": "to_oncall", "type": "transfer", "label": "On-call phone", "position": { "x": 600, "y": 100 },
      "config": { "number": "+919800000001", "ring_seconds": 25, "caller_id": "+919800000002" }
    },
    {
      "id": "closed", "type": "hangup", "label": "Closed", "position": { "x": 400, "y": 200 },
      "config": { "greeting_text": "We are closed. Please call again between 9 am and 6 pm, Monday to Friday." }
    },
    {
      "id": "sorry", "type": "hangup", "label": "No one available", "position": { "x": 1000, "y": 0 },
      "config": { "greeting_text": "Sorry, no one is available right now. Please try again later." }
    },
    {
      "id": "bye", "type": "hangup", "label": "Goodbye", "position": { "x": 1000, "y": -200 },
      "config": { "greeting_text": "Thank you for calling. Goodbye." }
    }
  ],
  "edges": [
    { "from": "node_start",  "to": "hours",       "condition": "default" },
    { "from": "hours",       "to": "main_menu",   "condition": "in_hours" },
    { "from": "hours",       "to": "closed",      "condition": "out_of_hours" },
    { "from": "main_menu",   "to": "lookup",      "condition": "digit:1" },
    { "from": "main_menu",   "to": "to_oncall",   "condition": "digit:2" },
    { "from": "main_menu",   "to": "to_support",  "condition": "timeout" },
    { "from": "main_menu",   "to": "to_support",  "condition": "max_retries" },
    { "from": "lookup",      "to": "to_crm_team", "condition": "http:2xx" },
    { "from": "lookup",      "to": "to_support",  "condition": "http:non2xx" },
    { "from": "to_crm_team", "to": "to_support",  "condition": "no_answer" },
    { "from": "to_crm_team", "to": "bye",         "condition": "completed" },
    { "from": "to_support",  "to": "sorry",       "condition": "no_answer" },
    { "from": "to_support",  "to": "bye",         "condition": "completed" },
    { "from": "to_oncall",   "to": "to_support",  "condition": "no_answer" }
  ]
}
```

Save the definition above as `definition.json` and create the flow from your server (the API key never goes to a browser):

```bash
jq -n --slurpfile def definition.json \
  '{name: "Main line", is_active: true, definition: $def[0]}' |
curl -sS -X POST https://api.example.com/v1/flows \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d @-
```

Then attach the flow to an inbound number, pass its id to `POST /v1/guest-credentials`, or set it as the tenant's default flow.
