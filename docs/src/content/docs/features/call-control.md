---
title: Call control
description: Hold, resume, blind transfer and supervision of live calls, from the agent SDK and from your backend.
---

Tring has two control paths:

| Path | Who uses it | Auth | Controls |
|---|---|---|---|
| **Agent SDK** | The agent's browser | Agent token | The agent's **own** call |
| **Backend REST** | Your server | Tenant API key | Any call of the tenant, by id |

Supervision (listen, whisper, barge) is console-only. Each action is scoped to one tenant; an agent can never act on someone else's call.

## From the agent SDK

The [agent widget](/tring/sdk/agent-widget/) shows Hold, Transfer, mute and keypad buttons. With the [JavaScript API](/tring/sdk/javascript-api/), the active call object exposes the same actions:

```js
call.hold();                          // customer hears hold music, agent is muted
call.resume();                        // back to the conversation
call.transfer({ toTeam: "billing" }); // or { toSip: "crm-user-22" }
```

These are messages on the call's `/rtc` socket, handled by the gateway. No API key and no backend proxy are involved.

### Hold and resume

- **Hold:** the agent's microphone is muted in the browser, the tenant's hold music is played to the other party, and the agent hears neither the music nor the customer.
- **Resume:** the music stops and audio flows both ways again.

If the tenant has no playable hold-music file, the held party hears silence. See [Sounds and hold music](/tring/features/sounds-and-hold-music/).

### Blind transfer

`transfer` takes exactly one target: `toSip` (another agent's `sip_username`) or `toTeam` (a team name). The agent doesn't talk to the target first.

The gateway checks the target first. If the agent is unreachable now (offline, Away or busy), the team doesn't exist, or the agent names themselves, the transfer is rejected with `transfer-failed` and **the agent keeps the call**.

**To an agent** (`toSip`), the transfer has a fallback, so the agent doesn't lose the call if nobody picks up:

1. The agent gets `transfer-ok` and **stays on the call** while the target rings. The customer hears hold music. The agent is muted and hears nothing; this is done by the gateway, so it also applies to a transfer started over REST or after a page refresh. The widget shows "Transferring…".
2. The target rings for up to the tenant's **transfer timeout** (Settings, or `transfer_timeout_seconds` on `PATCH /v1/tenants/me`; 60 seconds by default).
3. When the target answers, the agent gets `ended` (reason `transferred`) and leaves; the customer is talking to the target.
4. If nobody answers in time, **the call goes back to the agent**: they are unmuted and hear the customer again, and get `transfer-failed` with reason `no answer`. The customer hears no failure message.

**To a team** (`toTeam`), the call goes to the team's **queue**: the agent gets `transfer-ok` and then `ended` (reason `transferred`) straight away, and the customer waits in line with the team's hold music, like any other caller for that team, until a member answers. The team's strategy, ring time and `max_wait` apply (see [Agents and routing](/tring/features/agents-and-routing/)); the team doesn't need a free member when the transfer starts.

If nobody answers and there is no agent to give the call back to (the team's `max_wait` ran out, or the agent hung up while their transfer rang):

- If the call came through an IVR Transfer node, it goes back to that node, which takes its `queue_timeout` edge (the team's wait ran out; `no_answer` when that edge isn't wired) or its `no_answer` edge. The flow decides what the customer hears next.
- Otherwise the customer hears the tenant's **transfer failed message** (Settings, or `transfer_failed_message` on `PATCH /v1/tenants/me`, synthesized to speech when saved), then the call ends.

If the customer hangs up while the target rings, the ring stops and the call ends.

If the call came through an IVR Transfer node and the transfer is answered, the flow's `completed` step (a post-call survey, say) runs when the agent who finally has the call hangs up with the customer still on the line, as it does without a transfer.

Only one transfer can run per call at a time. The transfer is recorded on the call's timeline.

The widget's target picker lists online, available agents (by name) and all teams from `GET /v1/sdk/transfer-targets`, called with the agent token.

Attended (consult) transfer and conferencing are not available yet.

### DTMF

Keypresses from the widget's keypad go to the call's IVR (for menus and gathers), on the same socket.

## From your backend

```bash
# Put a call on hold, then resume it
curl -sS -X POST https://api.example.com/v1/calls/<call-id>/hold \
  -H "Authorization: Bearer $TRING_API_KEY"
curl -sS -X POST https://api.example.com/v1/calls/<call-id>/resume \
  -H "Authorization: Bearer $TRING_API_KEY"
```

The call must be `answered` (else `409`). Hold plays the tenant's hold music, or FreeSWITCH's built-in music, into the customer's leg on FreeSWITCH. Response: `{"id": "<call-id>", "held": true}`.

:::caution
The REST hold and resume endpoints act on a call's **FreeSWITCH** leg. Calls now run on the media gateway (browser visitor calls, phone calls, IVR transfers, `POST /v1/calls` click-to-calls), which have no such leg, so REST hold and resume fail for them. Use the agent SDK controls above to hold a call.
:::

The agent-token REST endpoints `POST /v1/sdk/calls/hold` and `POST /v1/sdk/calls/resume` do the same on the agent's own newest answered call, resolved from the token, with the same limitation.

`POST /v1/sdk/calls/transfer` (`{"to_sip": …}` or `{"to_team": …}`, agent token) blind-transfers the agent's own newest answered call through the media gateway. It works for web, phone and click-to-call calls and behaves exactly like the widget's Transfer button (see [Blind transfer](#blind-transfer)): an unreachable target leaves the agent on the call with `409`; otherwise the target rings (or, for a team, the call goes to its queue). See the [agent widget endpoints](/tring/sdk/agent-widget/) for its error codes.

## Supervision

A console user permitted to supervise (see below) can join a live call silently and switch modes. This works on web, phone and WhatsApp calls: any call whose audio runs through the gateway, including click-to-calls placed with `POST /v1/calls` and `POST /v1/calls/connect`.

| Mode | Supervisor hears | Who hears the supervisor |
|---|---|---|
| `monitor` | Everyone | Nobody |
| `whisper` | Everyone | The agent only |
| `barge` | Everyone | Everyone |

1. From the live calls list, the console calls `POST /v1/calls/{id}/supervise`. This endpoint accepts **only a console session**, never an API key, because permission depends on the user's role and teams. The call must be live (`ringing` or `answered`) and have a media room.
2. Tring returns a single-use supervisor token (2 minutes) pinned to that call's room, plus `gateway_url` and ICE servers.
3. The console joins in `monitor` and can switch modes. The agent's widget shows that a supervisor is present, with the mode and name.

On a phone call, a supervisor can listen from the IVR onwards, before any agent has answered. An agent who answers later is shown the supervisor and hears a whisper already in progress. The phone caller never hears a supervisor in `monitor` or `whisper`.

Barge on a phone call needs the gateway to mix the agent's and the supervisor's audio into the one stream the caller receives. The `tring` image does this: it is built with libopus (`-tags opus`). A binary built without it, such as `go run ./cmd/tring`, logs a warning on barge, and the caller hears one of them at a time.

Every session is audited (who, which call, when, which modes) and appears on the call's timeline.

### Who can supervise

| Role | Can supervise |
|---|---|
| `admin`, `supervisor` | Any live call in the tenant. |
| Team manager (`is_manager` on a team) | Only calls that belong to a team they manage. |
| Everyone else | Nothing (`403 not permitted to supervise`). |

A call belongs to a team when that team rang it, or when the caller waited in that team's queue. Being the answering agent's teammate does not count. The old per-user `can_supervise` flag no longer exists; roles and team management replace it.
