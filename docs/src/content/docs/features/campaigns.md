---
title: Campaigns and voice blasts
description: Send uploaded audio, text-to-speech messages, or interactive IVR calls to a recipient list.
---

**Campaigns** in the admin console shares one scheduling and reporting workflow across four delivery types:

| Type | On answer |
|---|---|
| Uploaded audio | Play the prepared message once, then hang up. |
| Text-to-speech | Play a message generated with the installed Piper voice, then hang up. |
| Interactive IVR | Run a snapshot of an existing IVR flow, including keypad input and transfers. |
| Agent-assisted | Ring an available team agent in Tring first; once they accept, dial the client and connect the conversation. |

Campaign management is available to console administrators and `full` API keys. Agent/supervisor sessions and `calls` keys cannot access it.

## Create and launch

1. Open **Campaigns → New campaign**. Enter a name, choose content, and select an active caller ID belonging to your tenant.
2. Set daily calling hours and their IANA timezone, concurrency, calls per minute, maximum attempts, retry delay, ring timeout, and maximum call length.
3. Save the draft and choose **Prepare audio & preview**. Listen to the prepared audio. Preparation requires working ffmpeg; TTS also requires Piper and a model. An unavailable engine or invalid audio is an error, not a silent call.
4. Add recipients by CSV, pasted numbers, or API. CSV columns are `phone` and optional `external_ref`. Each import accepts up to 10,000 rows / 2 MB; invalid rows are reported and duplicate normalized numbers are skipped. Number validation currently supports Indian mobile numbers, consistent with other outbound calling endpoints.
5. Place a test call to a number you specify. This is a real, immediate call, outside campaign calling hours. Test outcomes appear separately and do not consume a recipient's attempts.
6. Start now or schedule for a future instant. The console's scheduled-start picker uses the browser's timezone; daily calling hours use the campaign's configured timezone. New calls wait for both the scheduled start and an open calling window.

Audio uploads use the existing sound library: WAV, MP3, OGG, or M4A, up to 10 MB. Preparation re-encodes even Ogg uploads to the required Opus format. Prepared files are retained separately from library sounds. Editing or deleting the source sound after preparation does not change the message.

TTS uses one static message and the configured voice. Spoken template variables and per-recipient speech are not supported. IVR flow definitions and their audio are copied during preparation. The current runtime does not implement `goto_flow`; campaign preparation rejects that node rather than silently ending the call. Changes to live agent/team availability, webhook configuration, and HTTP callback responses remain dynamic.

Saving a draft edit invalidates prepared content. Content and delivery settings become immutable when a campaign starts or is scheduled.

## Agent-assisted campaigns

Choose **Agent-assisted**, select a team, and set the maximum agent wait. **Verify team & prepare** validates the team without requiring audio or TTS. The team's live membership, ringing strategy, ring timeout, and queue timeout apply; the campaign's maximum agent wait also caps the wait. Agents must be online and available in their Tring app/widget. Agent phone-number dialing is not part of this mode.

The customer is not dialed until an agent accepts. Shared reservations prevent an agent being assigned to competing calls. The agent hears the configured tenant hold music while the customer rings; a customer answer connects the conversation and starts its call recording when enabled. An agent leaving before customer answer cancels the dial, and either party ending a connected call ends both legs.

If no agent accepts, the attempt is `no_answer` with reason `agent_not_answered`; no customer call is placed. This uses one attempt and follows the configured maximum attempts and retry delay. A test call also rings the selected team first. Completed means the customer connected to an agent and the conversation ended normally; exceeding the maximum call duration or losing gateway ownership is incomplete.

## Controls and outcomes

- **Pause** stops new calls. Calls already claimed by the gateway finish.
- **Resume** continues queued recipients, respecting the original future start and calling window.
- **Cancel** cancels pending and unclaimed calls. Active calls finish. Cancellation is terminal.
- **Completed** means all recipients have a terminal outcome, not that every recipient answered.

Busy, unanswered, and selected temporary carrier failures retry up to the configured maximum, with the configured delay. The maximum includes the first attempt. A completed or partially played message is not automatically retried.

Recipient outcomes are `pending`, `active`, `completed`, `incomplete`, `busy`, `no_answer`, `failed`, `unknown`, and `cancelled`. An attempt is initially `queued`, then `active`, then terminal. A recipient waiting for another attempt returns to `pending`; its earlier failure stays in attempt history.

**Answered** records carrier answer. **Completed** records finished message playback, a completed IVR walk, or an agent-assisted conversation that connected and ended normally. Neither proves a human listened. There is no answering-machine detection.

Reports provide recipient counts, attempt/answer/completion counts excluding tests, paginated attempt history, call-log links, and recipient CSV export. Exported cells are escaped to prevent spreadsheet formula execution.

An optional result webhook sends `campaign.attempt.finished` using the existing signed webhook delivery system. It includes campaign, recipient, attempt and call IDs, status, reason, answer flag, external reference, and whether it was a test. IVR node and call-end hooks remain available as configured in the flow. Webhooks use the existing best-effort submission and retry behavior; the campaign API remains the source of truth, including recovery outcomes after a gateway crash.

## Scheduling and recovery

Postgres stores definitions, recipient progress, frozen attempt payloads, and gateway ownership. API replicas coordinate allocation with a short transaction lock; gateway claims are atomic and permanent for each attempt. Dispatch retries reuse the same attempt ID.

The gateway also enforces call-start pacing, so an outage cannot turn queued requests into an uncontrolled burst. It heartbeats active attempts every 10 seconds. A lost heartbeat for two minutes settles the attempt as `unknown` (shown as **Needs review**) and closes its stale call-log row. An uncertain attempt is never automatically redialed. Inspect its history before creating a new campaign for that recipient.

Live media still cannot survive a gateway process restart. Prepared audio must be readable at the same shared `TRING_AUDIO_DIR` path by the API and gateway. Prepared campaign assets are retained; automatic retention/garbage collection is not provided yet.

## Capacity settings

These deployment-wide limits apply across campaigns and API replicas. Configure API and gateway roles consistently.

| Environment variable | Default | Meaning |
|---|---:|---|
| `TRING_CAMPAIGN_MAX_CONCURRENT` | 50 | Maximum reserved/active campaign attempts. |
| `TRING_CAMPAIGN_TENANT_CONCURRENT` | 10 | Per-tenant campaign cap; also bounds a tenant-owned trunk shared by campaigns. |
| `TRING_CAMPAIGN_STARTS_PER_SECOND` | 5 | Global allocation and actual gateway-start limit. |
| `TRING_CAMPAIGN_TOTAL_CALL_CAPACITY` | 100 | Capacity budget used by campaign admission. |
| `TRING_CAMPAIGN_RESERVED_CALLS` | 20 | Budget kept available for ordinary calls. |

Admission counts live calls and campaign reservations conservatively, so effective campaign throughput may be lower than these ceilings. The scheduler does not reject ordinary inbound calls. Start small and adjust after measuring your gateway and carrier limits.

See the [Campaigns API](/tring/api-reference/campaigns/) for integration details.
