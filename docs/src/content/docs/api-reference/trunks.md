---
title: Trunks
description: Create and manage per-tenant SIP trunks, choose REGISTER or IP authentication, and understand the FreeSWITCH gateway XML Tring renders from them.
---

A **trunk** is a SIP connection to a carrier. Tring stores trunks in Postgres and renders each active one to a FreeSWITCH gateway file, then asks FreeSWITCH to rescan its `external` profile. Changes take effect without restarting FreeSWITCH or dropping live calls.

Outbound calls leave on a trunk through an [outbound number](/tring/api-reference/numbers/) that references it with `trunk_id`. A trunk with no outbound number is never picked for outbound calls.

All routes on this page take a tenant API key or a console session. See [Authentication](/tring/api-reference/authentication/).

## Auth modes

The `register` flag picks how the carrier authenticates you.

| Mode | `register` | Fields used | Typical carrier |
|---|---|---|---|
| **REGISTER** | `true` | `sip_username`, `sip_password`, `sip_realm`; optionally `proxy_ip` and `extra_params.auth_username` | Aggregators and IMS carriers that issue SIP credentials. FreeSWITCH registers and keeps the registration alive: to the realm, or to `proxy_ip` when set. |
| **IP-authenticated** | `false` (default) | `proxy_ip`, optionally `sip_realm` | Private circuits where the carrier trusts your IP. No REGISTER, no credentials. Calls are sent to `proxy_ip`. |

`auth_acl_cidr` (the carrier's SBC range, for example `198.51.100.8/29`) is stored with the trunk for reference only. It is **not** rendered into FreeSWITCH. Allowing inbound INVITEs from that range is configured separately in FreeSWITCH's ACL.

## What gets rendered to FreeSWITCH

Each **active** trunk becomes one file, `t_<trunk-uuid>.xml`, in the dynamic gateway directory (`/etc/freeswitch/sip_profiles/external/dynamic` by default, overridable with `TRING_GATEWAY_DIR`). Inactive and deleted trunks have their files removed. The gateway name is `t_` plus the first 12 hex characters of the trunk UUID, for example `t_9a4c6e803333`.

A REGISTER trunk:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<include>
  <gateway name="t_9a4c6e803333">
    <param name="username" value="tring-trunk-01"></param>
    <param name="password" value="(from sip_password)"></param>
    <param name="realm" value="sip.example.com"></param>
    <param name="register" value="true"></param>
    <param name="expire-seconds" value="600"></param>
    <param name="retry-seconds" value="30"></param>
    <param name="ping" value="25"></param>
    <param name="caller-id-in-from" value="true"></param>
  </gateway>
</include>
```

An IP-authenticated trunk with `from_domain` set:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<include>
  <gateway name="t_4e1f0a2b4444">
    <param name="register" value="false"></param>
    <param name="proxy" value="198.51.100.10"></param>
    <param name="realm" value="198.51.100.10"></param>
    <param name="from-domain" value="203.0.113.10"></param>
    <param name="ping" value="25"></param>
    <param name="caller-id-in-from" value="true"></param>
  </gateway>
</include>
```

- In IP mode, `realm` is `sip_realm`, or `proxy_ip` when `sip_realm` is empty.
- In REGISTER mode, `realm` is `sip_realm`, or `proxy_ip` when `sip_realm` is empty. `proxy_ip` (when set) adds `<param name="proxy">`: REGISTER and calls go to that address while `realm` stays the SIP domain. Use it when the realm isn't resolvable from your network, as with a carrier's IMS domain. `extra_params.auth_username` adds `<param name="auth-username">`.
- `ping` makes FreeSWITCH send SIP OPTIONS every 25 seconds. That's how an IP-auth trunk's `status` is known (see below).
- `caller-id-in-from` puts each call's caller ID (the outbound number's DID) in the `From` user, so there's no trunk-level `from-user`.

:::note
Both the API container (which writes) and FreeSWITCH (which reads) must mount the same host directory at the gateway path, and FreeSWITCH's external profile must include `external/dynamic/*.xml`. If they don't, trunk saves succeed in the database but FreeSWITCH never sees them.
:::

## `extra_params`

`extra_params` is a free-form JSON object stored with the trunk. Tring reads these keys from it:

| Key | Type | Rendered as | Use |
|---|---|---|---|
| `from_domain` | string | `<param name="from-domain">` | Sets the host part of `From` on calls, OPTIONS pings and REGISTER. Some carriers require your own circuit IP there rather than their realm. |
| `auth_username` | string | `<param name="auth-username">` | REGISTER mode: the digest username when it differs from `sip_username`. IMS carriers register a public identity (`sip_username`) but authenticate a private one (`auth_username`). |

Other keys are stored and returned but ignored. A missing or malformed value is simply left out of the XML.

## The trunk object

| Field | Type | Description |
|---|---|---|
| `id` | UUID | Trunk ID. |
| `tenant_id` | UUID | Owning tenant. |
| `name` | string | Your label. Unique within the tenant. |
| `provider_type` | string | Drives destination-number normalisation (see below). Default `generic`. |
| `sip_username` | string | REGISTER mode username. |
| `sip_password` | string | REGISTER mode password. Returned by create, get and update; left out of list responses. |
| `sip_realm` | string | REGISTER realm, or the IP-mode realm override. |
| `register` | boolean | `true` = REGISTER auth, `false` = IP auth. |
| `proxy_ip` | string | IP mode: the carrier SBC to send calls to. REGISTER mode (optional): the registrar/SBC to send REGISTER and calls to. |
| `auth_acl_cidr` | string | The carrier's SBC range, stored for reference. |
| `active` | boolean | Only active trunks are rendered to FreeSWITCH. |
| `extra_params` | object | See above. |
| `created_at` | timestamp | |
| `updated_at` | timestamp | |
| `status` | string | List responses only. Live health: `online`, `offline`, `connecting`, `not_loaded` or `unknown`. |
| `state_raw` | string | List responses only. FreeSWITCH's raw view, for example `REGED ping=UP 2.74ms` or `NOREG ping=DOWN`. Omitted when FreeSWITCH has no such gateway. |
| `reg_state` | string | List responses only. Registration state: `REGED` (registered), `NOREG` (not registering, normal for IP auth), `TRYING`, `FAIL_WAIT`, `NOAUTH`, `EXPIRED`, … Omitted when FreeSWITCH has no such gateway. |
| `ping_ms` | number | List responses only. Last OPTIONS round trip in milliseconds. Omitted until a ping is answered. |
| `calls` | object | List responses only. Calls through this trunk since FreeSWITCH (re)loaded it: `out`, `failed_out`, `since`, plus `in` and `failed_in` for **registered** trunks only (FreeSWITCH doesn't attribute inbound calls to IP-auth trunks). Omitted when FreeSWITCH has no such gateway. |

How `status` is worked out:

- **REGISTER trunks:** from the registration state. `REGED` is `online`; `TRYING`/`REGISTER` is `connecting`; failed or unregistered states are `offline`.
- **IP-auth trunks:** from the OPTIONS ping. `online` needs an answered ping with a measured round trip; up but not yet measured is `connecting`; no answer is `offline`.
- `not_loaded` means FreeSWITCH has no gateway by that name: the trunk is inactive, the gateway file wasn't picked up, or the API couldn't reach FreeSWITCH.

### `provider_type`

`provider_type` decides how destination numbers are normalised before dialing on this trunk (the first-choice trunk's type is used for the whole call):

| Value | Normalisation |
|---|---|
| `exotel`, `airtel`, `jio`, `tata` | Indian mobile numbers only. Accepts `+91…`, `91…`, `0…` and bare 10-digit forms, with spaces or dashes. Normalises to `+91XXXXXXXXXX`. Anything else is rejected with `400`. |
| `generic` (default), `loopback`, or any other value | None. The trimmed input is dialed as is. |

:::danger
With `generic` there is no destination validation at all. Whatever reaches `POST /v1/calls` gets dialed on your trunk and billed to you. Never pass a number an end user typed straight through: validate it and check it against an allow-list in your backend first. This is how toll fraud happens.
:::

### POST /v1/trunks

Create a trunk, then re-render every gateway file and rescan FreeSWITCH.

**Auth:** tenant API key or console session.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Label, unique within the tenant. |
| `provider_type` | string | no | Default `generic`. See the table above. |
| `register` | boolean | no | Default `false` (IP auth). |
| `sip_username` | string | REGISTER mode | |
| `sip_password` | string | REGISTER mode | |
| `sip_realm` | string | REGISTER mode | In IP mode, optional realm override. |
| `proxy_ip` | string | IP mode; optional in REGISTER mode | Carrier SBC address. In REGISTER mode, the registrar to send REGISTER and calls to. |
| `auth_acl_cidr` | string | no | Carrier SBC range, for reference. |
| `active` | boolean | no | Default `true`. |
| `extra_params` | object | no | Default `{}`. See [`extra_params`](#extra_params). |

The API doesn't enforce the per-mode fields. A REGISTER trunk with no credentials, or an IP trunk with no `proxy_ip`, is saved and rendered as is.

```bash
curl -sS -X POST "$TRING_URL/v1/trunks" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "carrier-a-primary",
    "provider_type": "tata",
    "register": false,
    "proxy_ip": "198.51.100.10",
    "auth_acl_cidr": "198.51.100.8/29",
    "extra_params": {"from_domain": "203.0.113.10"}
  }'
```

For a REGISTER trunk, read the password from a secret store rather than typing it into a command:

```bash
curl -sS -X POST "$TRING_URL/v1/trunks" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d "$(jq -n --arg pw "$CARRIER_SIP_PASSWORD" '{
    name: "aggregator-b",
    provider_type: "exotel",
    register: true,
    sip_username: "tring-trunk-01",
    sip_password: $pw,
    sip_realm: "sip.example.com"
  }')"
```

**Response:** `201 Created` with the trunk object (including `sip_password`, without `status`).

```json
{
  "id": "4e1f0a2b-4444-4a00-8d00-000000000001",
  "tenant_id": "7d3e9f10-0000-4000-8000-000000000001",
  "name": "carrier-a-primary",
  "provider_type": "tata",
  "sip_username": "",
  "sip_realm": "",
  "register": false,
  "proxy_ip": "198.51.100.10",
  "auth_acl_cidr": "198.51.100.8/29",
  "active": true,
  "extra_params": {"from_domain": "203.0.113.10"},
  "created_at": "2026-10-16T10:02:31.220114+05:30",
  "updated_at": "2026-10-16T10:02:31.220114+05:30"
}
```

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid JSON body` |
| `400` | `name is required` |
| `409` | `trunk insert failed: …` with the database error. A duplicate `name` in your tenant lands here. |
| `502` | `trunk saved but FreeSWITCH did not reload: … — retry with POST /v1/trunks/reload`. The trunk **is** saved; FreeSWITCH just hasn't picked it up. |

### GET /v1/trunks

List your tenant's trunks, sorted by name, with live health. Not paginated.

**Auth:** tenant API key or console session.

**Query parameters:** none.

`sip_password` is left out of every item. Health comes from FreeSWITCH on each request. If FreeSWITCH can't be reached, trunks show `not_loaded` rather than the request failing.

```bash
curl -sS "$TRING_URL/v1/trunks" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `200 OK`

```json
[
  {
    "id": "4e1f0a2b-4444-4a00-8d00-000000000001",
    "tenant_id": "7d3e9f10-0000-4000-8000-000000000001",
    "name": "carrier-a-primary",
    "provider_type": "tata",
    "sip_username": "",
    "sip_realm": "",
    "register": false,
    "proxy_ip": "198.51.100.10",
    "auth_acl_cidr": "198.51.100.8/29",
    "active": true,
    "extra_params": {"from_domain": "203.0.113.10"},
    "created_at": "2026-10-16T10:02:31.220114+05:30",
    "updated_at": "2026-10-16T10:02:31.220114+05:30",
    "status": "online",
    "state_raw": "NOREG ping=UP 2.74ms",
    "reg_state": "NOREG",
    "ping_ms": 2.74,
    "calls": {"out": 5, "failed_out": 5, "since": "2026-10-19T08:00:00Z"}
  }
]
```

**Errors**

| Status | Message |
|---|---|
| `500` | `query failed` / `scan failed` |

### GET /v1/trunks/{id}

Fetch one trunk.

**Auth:** tenant API key or console session.

Unlike the list, this response **includes `sip_password`** and has no `status`/`state_raw`.

```bash
curl -sS "$TRING_URL/v1/trunks/4e1f0a2b-4444-4a00-8d00-000000000001" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `200 OK` with the trunk object.

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid id` |
| `404` | `trunk not found` |
| `500` | `query failed` |

:::caution
Trunk passwords are stored in plain text and come back from create, get and update. Anyone holding an API key or console session for the tenant can read them. Keep API keys in a secret manager, avoid logging these responses, and rotate the carrier password if a key leaks.
:::

### PATCH /v1/trunks/{id}

Update any trunk fields, then re-render and rescan.

**Auth:** tenant API key or console session.

**Request body.** Every field is optional; leave a field out to keep its current value.

| Field | Type | Description |
|---|---|---|
| `name` | string | |
| `provider_type` | string | |
| `sip_username` | string | |
| `sip_password` | string | |
| `sip_realm` | string | |
| `register` | boolean | Switch auth mode. |
| `proxy_ip` | string | |
| `auth_acl_cidr` | string | |
| `active` | boolean | `false` removes the gateway from FreeSWITCH. |
| `extra_params` | object | Replaces the whole object. It is not merged. |

```bash
curl -sS -X PATCH "$TRING_URL/v1/trunks/4e1f0a2b-4444-4a00-8d00-000000000001" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"active": false}'
```

**Response:** `200 OK` with the updated trunk object.

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid id` |
| `400` | `invalid JSON body` |
| `404` | `trunk not found` |
| `500` | `update failed: …` (including a duplicate `name`) |
| `502` | `trunk updated but FreeSWITCH did not reload: … — retry with POST /v1/trunks/reload` |

### DELETE /v1/trunks/{id}

Delete a trunk and remove its gateway file. Numbers that referenced it keep existing with `trunk_id` set to `null`. An outbound number with no trunk is skipped when picking routes.

**Auth:** tenant API key or console session.

```bash
curl -sS -X DELETE "$TRING_URL/v1/trunks/4e1f0a2b-4444-4a00-8d00-000000000001" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `204 No Content`.

**Errors**

| Status | Message |
|---|---|
| `400` | `invalid id` |
| `404` | `trunk not found` |
| `500` | `delete failed` |
| `502` | `trunk deleted but FreeSWITCH did not reload: … — retry with POST /v1/trunks/reload` |

### POST /v1/trunks/reload

Re-render every active trunk's gateway file and rescan FreeSWITCH's external profile. Use it after a `502` from a trunk write, or whenever FreeSWITCH has drifted from the database (for example after a FreeSWITCH restart with a stale gateway directory).

This re-renders the gateways of **every tenant** on the deployment, not just yours. It's safe to repeat: the rescan doesn't drop live calls.

**Auth:** tenant API key or console session.

**Request body:** none.

```bash
curl -sS -X POST "$TRING_URL/v1/trunks/reload" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response:** `200 OK`

```json
{ "status": "reloaded" }
```

**Errors**

| Status | Message |
|---|---|
| `500` | `reload failed: …` with the file-write or FreeSWITCH error. |

:::danger
Only open your SIP port (`5060`) to your carriers' published IP ranges, never `0.0.0.0/0`, and keep FreeSWITCH's ESL port (`8021`) private. An IP-authenticated trunk trusts the network path, so a wide-open SIP port invites toll fraud.
:::
