---
title: Numbers and trunks
description: Inbound, outbound and masking numbers, SIP trunks with REGISTER or IP authentication, outbound trunk strategies and the caller-ID rule.
---

A **trunk** is your connection to a phone carrier. A **number** is a phone number (DID) you hold on a trunk. Phone calls enter and leave Tring through FreeSWITCH, which is driven entirely by the trunks and numbers you configure.

## Numbers

| Field | What it is |
|---|---|
| `e164` | The number, written exactly as the carrier sends it, normally with the leading `+` (for example `+91XXXXXXXXXX`). |
| `type` | `inbound`, `outbound` or `masking`. |
| `trunk_id` | The trunk the number belongs to. **Required for `outbound`.** |
| `flow_id` | The IVR flow that runs when this number is called (inbound). |
| `priority` | Order among outbound numbers, lowest first. Default `100`. |
| `active` | Inactive numbers are ignored. |
| `provider_did_id` | Optional carrier reference for your records. |

```bash
curl -sS -X POST https://api.example.com/v1/numbers \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"e164":"+919800000001","type":"inbound","flow_id":"<flow-uuid>"}'
```

Update with `PATCH /v1/numbers/{id}`: `priority`, `active`, `flow_id`, or `"clear_flow": true` to detach the flow. Tring refuses (`409`) to deactivate your **last** active outbound number, because every outbound call would then fail. See the [Numbers API](/tring/api-reference/numbers/).

### Types

| Type | Used for |
|---|---|
| `inbound` | Calls **to** this number run its `flow_id`. |
| `outbound` | The caller IDs (and trunks) outbound calls go out on, chosen by the tenant's [outbound strategy](#outbound-trunk-strategies). |
| `masking` | A number you hold for presenting as caller ID (see [the caller-ID rule](#caller-id-ownership)). It does not route inbound calls and is not picked as an outbound trunk. |

### Inbound lookup is an exact match

When a call arrives, Tring looks up the dialed number (the SIP `To` user) among `inbound` numbers with an exact string match (this lookup does not check the `active` flag), then runs that number's flow. There is no normalization, so `+919800000001`, `919800000001` and `09800000001` are three different numbers. Register the number in the exact form your carrier sends. If no inbound number matches, or it has no flow, the call is rejected.

An inbound number belongs to one tenant: Tring refuses to register an inbound number another tenant on the same deployment already holds, and a number only ever runs a flow of its own tenant.

Inbound phone calls need the SIP plug enabled (`TRING_FSPLUG_ENABLED=true`); see [Configuration](/tring/getting-started/configuration/#inbound-phone-calls-sip-plug).

## Trunks

| Field | What it is |
|---|---|
| `name` | Your label. |
| `provider_type` | `generic` (default), `airtel`, `jio` or `tata`. |
| `register` | `true` for REGISTER (username/password) authentication, `false` for IP authentication. |
| `sip_username`, `sip_password`, `sip_realm` | REGISTER credentials and the registrar realm. |
| `proxy_ip` | IP authentication: the carrier's SBC address calls are sent to. REGISTER (optional): the registrar to send REGISTER and calls to, when the realm isn't resolvable. |
| `auth_acl_cidr` | IP authentication: the carrier's address range, stored for reference. |
| `extra_params` | JSON for extra settings: `from_domain` (host in `From`) and `auth_username` (REGISTER digest user, when different). |
| `active` | Inactive trunks are not loaded into FreeSWITCH. |

Saving, updating or deleting a trunk writes FreeSWITCH gateway XML and rescans the trunk profile; live calls stay up. If FreeSWITCH could not reload, the API answers `502` even though the change was saved. Retry with `POST /v1/trunks/reload`. List responses include a live `status`: `online`, `offline`, `connecting`, `not_loaded` or `unknown`. Passwords are never returned in list responses. See the [Trunks API](/tring/api-reference/trunks/).

### REGISTER or IP authentication

| | REGISTER | IP authentication |
|---|---|---|
| Typical for | Aggregators that issue SIP credentials | Direct carrier circuits (for example Tata) |
| You set | `sip_username`, `sip_password`, `sip_realm`; optionally `proxy_ip` and `auth_username` | `proxy_ip` (and optionally `sip_realm`, defaulting to `proxy_ip`) |
| FreeSWITCH | Registers every 600 s, retries every 30 s, to the realm or `proxy_ip` | Sends calls to `proxy_ip`, no registration |

Both kinds send a SIP OPTIONS keepalive every 25 seconds, which is what drives the `online`/`offline` status for IP-authenticated trunks. Each call puts its caller-ID number in the SIP `From` user.

### IMS carriers (registered trunks)

IMS carriers (for example Tata's IMS core) often need a registered trunk where:

- the **SIP username** is the public identity, for example `+91XXXXXXXXXX`;
- the **realm** is their IMS domain, which usually isn't resolvable from your network;
- the **auth username** is a separate private identity;
- REGISTER and calls go to their **SBC IP**.

Set `register: true`, `sip_username`, `sip_password`, `sip_realm` (the IMS domain), `proxy_ip` (the SBC) and `extra_params.auth_username`. Some carriers register with only `proxy_ip`, `sip_username` and `sip_password`; leave `sip_realm` empty and it defaults to `proxy_ip`. Use the exact values and number format your carrier provides. The Trunks page shows the registration state under each trunk's status, for example `registered · ping 2.7 ms` or `registration failed (FAIL_WAIT)`. Below it, a calls line (for example `calls out: 5 tried, 5 failed · last 2 h`) shows whether calls actually work: a trunk can answer every keepalive and still fail every call when the carrier hasn't finished provisioning it. When every outbound call has failed, a **calls failing** badge appears next to the status.

### `from_domain`

Some carriers check the host part of the `From` header and expect your own circuit IP rather than their realm. Set it in `extra_params`:

```json
{ "extra_params": { "from_domain": "198.51.100.20" } }
```

### Providers

`provider_type` picks the number rules used for outbound calls placed through the api (`POST /v1/calls`, `POST /v1/calls/connect`):

| Provider | Destination numbers |
|---|---|
| `airtel`, `jio`, `tata` | Indian numbers, normalized to `+91XXXXXXXXXX`. Accepts `+91`, `91`, `0`-prefixed and bare 10-digit forms of mobile numbers starting 6–9. Anything else is rejected before it reaches the carrier. |
| `generic` | No normalization; the number is passed as given. |

Outbound calls dialed from the media plane (an IVR [transfer to a number](/tring/features/ivr-flows/#transfer)) always use the Indian rules.

:::caution[Keep FreeSWITCH private]
Only open the trunk profile's SIP port and RTP range to your carrier's IP ranges. Trunk credentials are stored in the database in plain text; restrict database access accordingly.
:::

## Outbound trunk strategies

The tenant setting `outbound_strategy` (`PATCH /v1/tenants/me`) decides which outbound numbers a call uses. Active outbound numbers on active trunks are ordered by `priority`, then creation time.

| Strategy | Behaviour |
|---|---|
| `failover` (default) | Try every outbound number in order. FreeSWITCH moves to the next one only on the hangup causes listed in `failover_causes`. |
| `round_robin` | One number per call, rotating. The counter is in memory and resets on restart. |
| `manual` | Always the lowest-priority-value number. Switch primary by changing `priority` or `active`. |
| `load_based` | Accepted by the API but **not implemented**; calls fail with an error. Do not use it. |

```bash
curl -sS -X PATCH https://api.example.com/v1/tenants/me \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"outbound_strategy":"failover","failover_causes":["NORMAL_TEMPORARY_FAILURE","NO_ROUTE_DESTINATION"]}'
```

Each attempt presents its own trunk's number as caller ID, unless you override it (below).

:::note
Calls dialed from the media plane (IVR transfer to a number) use only the **first** selected trunk; multi-trunk failover on that path is not wired yet.
:::

## Caller-ID ownership

Wherever you can choose the caller ID (`caller_id` on `POST /v1/calls`, `caller_id` on an IVR transfer to a number), it **must be one of your tenant's active numbers** of any type: inbound, outbound or masking. Anything else is refused (`400` from the API; `no_answer` in a flow). This stops a tenant from presenting numbers it does not hold. Leave `caller_id` empty to use the trunk's own number.
