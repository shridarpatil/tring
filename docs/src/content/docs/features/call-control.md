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

`transfer` takes exactly one target: `toSip` (another agent's `sip_username`) or `toTeam` (a team name).

1. The gateway checks the target first. If it is unreachable now (agent offline or busy, team with nobody available), or the agent names themselves, the transfer is rejected with `transfer-failed` and **the agent keeps the call**.
2. Otherwise the agent gets `transfer-ok` and leaves the call immediately (blind transfer). The customer stays connected and hears hold music.
3. The routing engine rings the target into the same call, with the team's strategy and queue if it is a team (see [Agents and routing](/tring/features/agents-and-routing/)).
4. If nobody answers, the customer hears the tenant's **transfer failed message** (Settings, or `transfer_failed_message` on `PATCH /v1/tenants/me`, synthesized to speech when saved), then the call ends.

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

:::note
The REST hold, resume and transfer endpoints act on a call's **FreeSWITCH** leg. For calls that live only on the gateway (browser visitor calls, IVR transfers, `POST /v1/calls` click-to-calls), use the agent SDK controls above.
:::

The agent-token REST endpoints `POST /v1/sdk/calls/hold`, `POST /v1/sdk/calls/resume` and `POST /v1/sdk/calls/transfer` (`{"to_sip": …}` or `{"to_team": …}`) do the same on the agent's own newest answered call, resolved from the token. A team transfer through this path rings all reachable members at once. See the [Calls API](/tring/api-reference/calls/).

## Supervision

A console user with the supervise permission can join a live call silently and switch modes. This works on web, phone and WhatsApp calls: any call whose audio runs through the gateway, including click-to-calls placed with `POST /v1/calls` and `POST /v1/calls/connect`.

| Mode | Supervisor hears | Who hears the supervisor |
|---|---|---|
| `monitor` | Everyone | Nobody |
| `whisper` | Everyone | The agent only |
| `barge` | Everyone | Everyone |

1. From the live calls list, the console calls `POST /v1/calls/{id}/supervise`. This endpoint accepts **only a console session**, never an API key, because the permission belongs to a user. The call must be live (`ringing` or `answered`) and have a media room.
2. Tring returns a single-use supervisor token (2 minutes) pinned to that call's room, plus `gateway_url` and ICE servers.
3. The console joins in `monitor` and can switch modes. The agent's widget shows that a supervisor is present, with the mode and name.

On a phone call, a supervisor can listen from the IVR onwards, before any agent has answered. An agent who answers later is shown the supervisor and hears a whisper already in progress. The phone caller never hears a supervisor in `monitor` or `whisper`.

Barge on a phone call needs the gateway to mix the agent's and the supervisor's audio into the one stream the caller receives. The gateway image does this: it is built with libopus (`-tags opus`). A gateway built without it, such as `go run ./cmd/gateway` or a release binary, logs a warning on barge, and the caller hears one of them at a time.

Every session is audited (who, which call, when, which modes) and appears on the call's timeline. The first console admin created at bootstrap has the supervise permission; other users do not by default.
