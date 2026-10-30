---
title: Outbound calls
description: Click-to-call from your backend with POST /v1/calls and POST /v1/calls/connect, caller-ID override, and the number masking pattern.
---

Outbound calls are always started by **your backend** with a tenant API key. The browser SDK cannot place calls: an agent's own button calls your backend, which calls Tring. That way the agent never sees the dialed number, and a stolen agent token cannot place billable calls.

Tring has two click-to-call endpoints. Both are placed by the media gateway over the first-choice trunk of the tenant's [outbound strategy](/tring/features/numbers-and-trunks/#outbound-trunk-strategies), so they're recorded and supervisable like inbound calls.

| | `POST /v1/calls` | `POST /v1/calls/connect` |
|---|---|---|
| Agent side | A Tring agent in the browser (by `agent_id`) | Any phone number (`agent_number`) |
| Customer side | `to` | `client_number` |
| Who rings first | The agent's browser leg, which **auto-answers** (the agent clicked to call), then the customer | The agent's phone, then the customer once the agent answers |
| Caller ID | Trunk's number, or `caller_id` | Trunk's number on both legs |
| Agent sees | `agent_label` (or a generic incoming call) | Your trunk's number on their phone |
| Recorded | Yes, when the tenant records calls | Yes, when the tenant records calls |

Both return `201` with the new call record (status `ringing`). They don't run an IVR flow, so they send no [webhooks](/tring/features/webhooks/). Details are in the [Calls API](/tring/api-reference/calls/).

## Click-to-call to a browser agent

```bash
curl -sS -X POST https://api.example.com/v1/calls \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
        "agent_id": "<agent-uuid>",
        "to": "+919800000001",
        "agent_label": "Ticket #4821",
        "caller_id": "+919800000002"
      }'
```

| Field | Required | What it does |
|---|---|---|
| `agent_id` | yes | The agent's UUID or `sip_username`. |
| `to` | yes | Destination, normalized to an Indian `+91` mobile number. An invalid number is rejected with `400` before anything is dialed. |
| `agent_label` | no | Non-sensitive text shown to the agent, such as a ticket or account reference. The agent never receives `to`. |
| `caller_id` | no | The number the customer sees, instead of the trunk's own number. Must be one of your tenant's active numbers ([caller-ID rule](/tring/features/numbers-and-trunks/#caller-id-ownership)). |

The agent must have the agent widget open and connected, and not be on another call; otherwise the API answers `409` and nothing is dialed. The customer is dialed only after the agent's widget has joined, and the agent hears your hold music while the customer's phone rings. The call is `answered` when the customer picks up.

## Connect two phones

`POST /v1/calls/connect` bridges two phone numbers. Neither side needs a browser. Use it for callbacks where the agent works from a mobile phone.

```bash
curl -sS -X POST https://api.example.com/v1/calls/connect \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"agent_number":"+919800000003","client_number":"+919800000001"}'
```

Both numbers are normalized to Indian `+91` mobile numbers. Both legs show your primary trunk's number. While the client's phone rings, the agent hears your hold music. This endpoint has **no** `caller_id` override, and it uses only the first selected trunk. The call record has no `agent_id`; a call that never connects ends `failed` with a `hangup_cause` saying which side didn't answer ([details](/tring/api-reference/calls/#post-v1callsconnect)).

## Number masking

Masking keeps both parties' real numbers private: the customer only ever sees your numbers, and the agent only sees a label. Tring does not run a masking pool for you yet; you keep the mapping in your backend and use these building blocks:

| Building block | Where |
|---|---|
| Choose the number the customer sees | `caller_id` on `POST /v1/calls`, or on an IVR [transfer to a number](/tring/features/ivr-flows/#transfer) |
| Hide the customer's number from agents | `agent_label` on `POST /v1/calls`; `label` on a Transfer node |
| Look up who is calling which number | IVR `{{caller}}` and `{{did}}` variables sent to your backend with an HTTP callback |

A typical setup:

1. **Support DID.** Your public number, registered as an `inbound` number with your main flow.
2. **Mask DIDs.** A set of numbers you own. Register each one you want callers to be able to dial back as `inbound` with a "mask" flow (only `inbound` numbers route calls in; a `masking`-type number is accepted as caller ID but does not route inbound calls).
3. **Backend mapping.** When an agent calls a customer, your backend picks a mask DID, stores `mask DID + customer number → agent`, and calls `POST /v1/calls` with `caller_id` set to that mask DID and `agent_label` set to a reference.
4. **Call-back.** When the customer calls the mask DID back, the mask flow's HTTP callback sends `{{caller}}` and `{{did}}` to your backend, which answers with the agent and a label, for example `{"agent": "crm-user-17", "mask": "A-1042"}`.
5. The next Transfer node uses `"agent_sip": "{{agent}}"` and `"label": "Customer {{mask}}"`, with a `no_answer` edge to a team as fallback.

```json
{
  "id": "to_owner",
  "type": "transfer",
  "config": { "agent_sip": "{{agent}}", "label": "Customer {{mask}}" }
}
```

Expire mappings in your backend when a conversation ends, and protect the HTTP callback with a secret header.

:::note[Regulatory]
Present only numbers you hold, and follow your carrier's and regulator's caller-ID and consent rules. Tring enforces ownership of the caller ID but does not check consent or do-not-call lists.
:::

## Agent-dialed calls

The gateway's `/rtc` protocol has an agent-only `dial` message that dials a number into the agent's current call through the SIP plug, using the same trunk selection and caller-ID rule. Visitor connections can never use it. A room carries one outbound call at a time, and an agent can have at most two in progress; further `dial`s get `error` "a call is already in progress". The published SDK does not expose it; for click-to-call, use `POST /v1/calls`.
