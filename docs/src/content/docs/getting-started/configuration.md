---
title: Configuration
description: Every TRING_* environment variable in .env, what it does, its default, and what to set in production.
---

Tring is configured with environment variables. Docker Compose loads `.env` into every container. The Go binaries (`tring-api`, `tring-gateway`) read `TRING_*` variables over built-in defaults, and FreeSWITCH reads a few of the same variables from its `vars.xml`.

```bash
make .env   # or install.sh: copies .env.example and generates random secrets
```

The values in `.env.example` are tuned for **local development on one machine**. Delete a line to fall back to the built-in default. The sections below follow the sections of `.env.example`.

:::caution[Secrets]
`.env` holds credentials. Keep it out of version control, readable only by the deploy user (`chmod 600 .env`), and prefer a secret manager in production. The placeholder values shown on this page are not real secrets; replace every one of them.
:::

## 0. Timezone

| Variable | Default | What it does |
|---|---|---|
| `TZ` | `Asia/Kolkata` | IANA timezone for **every** container (api, gateway, FreeSWITCH, Postgres, Redis). The Go binaries embed the timezone database, so this works on the distroless images. It decides business hours for the IVR [Timing node](/tring/features/ivr-flows/#timing). |

## 1. Secrets

`make .env` and `install.sh` generate random values for `TRING_JWT_SECRET`, `TRING_SECRETS_KEY`, `TRING_FS_BASIC_AUTH`, `TRING_ESL_PASSWORD` and `TRING_ADMIN_PASSWORD`. The api and gateway **refuse to start** while any of their secrets is still the built-in placeholder or too short (`TRING_JWT_SECRET` at least 32 characters, the others at least 16).

| Variable | Default | What it does |
|---|---|---|
| `TRING_JWT_SECRET` | dev placeholder | HMAC-SHA256 key that signs **every** token Tring issues: console sessions, agent tokens, visitor call tokens and supervisor tokens. The api and the gateway must share the same value. |
| `TRING_SECRETS_KEY` | empty | api only. AES-256-GCM key that encrypts the SIP trunk and agent passwords stored in Postgres. Exactly 32 bytes, written as **64 hex characters** (`openssl rand -hex 32`; note `-hex 16` gives only 32 characters and is rejected) or base64. See [Encryption at rest](#encryption-at-rest) below. |
| `TRING_FS_BASIC_AUTH` | dev placeholder (`fs:…`) | `user:password` that FreeSWITCH presents to the api's `/fs/directory` endpoint (`mod_xml_curl`). FreeSWITCH reads the same variable from `.env`. |
| `TRING_FS_ADDR` | `127.0.0.1:8088` | Loopback-only listener for the api's `/fs/*` endpoints. They are not served on `TRING_API_ADDR`. Must match the URL in `xml_curl.conf.xml`. |
| `TRING_ALLOW_INSECURE_SECRETS` | `false` | Skip the startup secret check. Local development only. |

:::danger[Production]
- Anyone who knows `TRING_JWT_SECRET` can mint valid tokens for any tenant. Rotating it signs everyone out and invalidates every agent and visitor token in flight.
- FreeSWITCH reads `TRING_FS_BASIC_AUTH` and `TRING_ESL_PASSWORD` from the same `.env`, so changing them only needs a restart of the `freeswitch` and `api` containers.
:::

### Encryption at rest

FreeSWITCH needs the plaintext SIP passwords to register with carriers, so Tring can't hash them. Instead the api encrypts `trunks.sip_password` and `agents.sip_password` with `TRING_SECRETS_KEY` (AES-256-GCM, stored as `enc:v1:…`). It decrypts them only to write the FreeSWITCH gateway XML and to answer the agent directory lookup.

- **Key set:** on every start the api encrypts any plaintext passwords left in the database (one transaction; it logs how many, never the values). Setting the key on an existing install is how you migrate.
- **Key empty:** the api starts with a warning and keeps storing passwords in plaintext, so existing deployments keep working after an upgrade until you add the key. If the database already holds encrypted passwords, the api **refuses to start** without the key.
- **Malformed key** (not 32 bytes of hex or base64): the api refuses to start.
- **Wrong key:** the api starts, but each trunk whose password it can't decrypt is left out of the FreeSWITCH config with an error in the log (`trunk not rendered: cannot decrypt its sip_password`). Agent directory lookups for those agents fail the same way.

:::danger[Back up TRING_SECRETS_KEY]
Store the key in a password manager or secret store, **separately from your database backups** (a stolen DB dump alone doesn't reveal the passwords). If you lose it, the stored passwords can't be recovered: you must re-enter every trunk password in the console. Rotating the key is **not supported yet**, so don't change it once set.
:::

## 2. Database and cache

| Variable | Default | What it does |
|---|---|---|
| `TRING_DSN` | `postgres://tring:<password>@127.0.0.1:5434/tring` | Postgres connection string, used by the api and the gateway. The dev Compose stack publishes Postgres on port `5434`. The api applies schema migrations on startup. |
| `TRING_REDIS_ADDR` | `127.0.0.1:6379` in `.env.example`; empty built-in | Redis `host:port`, shared by the api and the gateway: flow cache, agent presence, queue depth, reservations, single-use call tokens and the dashboard live stream. Both binaries **must** use the same Redis. Empty disables all of these. |
| `TRING_REDIS_PASSWORD` | empty | Redis `AUTH` password, sent by every Redis client in the api and the gateway. The Compose `redis` service sets `requirepass` to the same value when it is non-empty. `make .env` / `install.sh` generate one; empty means no authentication. Use letters and digits only. |
| `TRING_LOG_FORMAT` | `text` | `text` (logfmt, readable locally) or `json` (for log shippers). |
| `TRING_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |

:::note
Redis is a **hard dependency** of the gateway when `TRING_GATEWAY_REQUIRE_AUTH=true` (the default): the gateway refuses to start without a reachable Redis, because it needs Redis to burn visitor call tokens single-use.
:::

In production, use a strong Postgres password, enable TLS on the connection where the database is remote (`?sslmode=require` or stricter), and bind Postgres and Redis to private interfaces only. The Compose stack publishes Redis on `127.0.0.1` only, but any process on the host can still reach that port, so set `TRING_REDIS_PASSWORD`. The startup secret check does not require it, because you may run Redis elsewhere behind network isolation instead.

To turn on Redis authentication for an existing deployment, add `TRING_REDIS_PASSWORD` to `.env`, then recreate Redis, the api and the gateway together: `docker compose up -d --force-recreate redis api gateway`. A Redis that requires a password the binaries don't send (or the reverse) makes them start with Redis-backed features disabled, and the gateway refuses to start when `TRING_GATEWAY_REQUIRE_AUTH=true`. Redis here is a cache with no persistence, so the restart only loses cached data, presence and in-flight queue state.

## 3. First-boot bootstrap

Seeds one tenant and one console admin the first time the api starts against an **empty** database. On every later start this is a no-op, and changing these values does **not** rotate any credential.

| Variable | Default | What it does |
|---|---|---|
| `TRING_BOOTSTRAP_TENANT` | `Default` | Name of the first tenant. |
| `TRING_ADMIN_EMAIL` | `admin@tring.local` | Console login email for the first admin. |
| `TRING_ADMIN_PASSWORD` | dev placeholder | Console password for the first admin (stored bcrypt-hashed). |

All three must be set for the seed to run. The seeded admin can supervise live calls. Set a strong password before the first start; after that, the value in `.env` is no longer used.

## 4. FreeSWITCH ESL

The api commands FreeSWITCH over its event socket (ESL) to place outbound phone calls, control FreeSWITCH-side legs and reload trunks. Browser calls do not use FreeSWITCH.

| Variable | Default | What it does |
|---|---|---|
| `TRING_ESL_ADDR` | `127.0.0.1:8021` | FreeSWITCH event socket address. |
| `TRING_ESL_PASSWORD` | FreeSWITCH's stock default (rejected at startup) | ESL password. FreeSWITCH reads it from `.env` and binds ESL to `127.0.0.1` only. |

:::danger
Never expose the ESL port to the internet. ESL gives full control of FreeSWITCH, including placing billable calls. Keep it on loopback or a private network and change the stock password.
:::

## 5. Call-plane gateway

The gateway (`cmd/gateway`) runs browser WebRTC on `/rtc`, the IVR, agent routing and call records.

### Browser-facing

| Variable | Default | What it does |
|---|---|---|
| `TRING_GATEWAY_PUBLIC_URL` | `http://127.0.0.1:8087` | The gateway base URL the api hands to browsers in agent tokens and visitor credentials. Browsers must be able to reach `/rtc` on it. Use `https://` in production. |
| `TRING_PUBLIC_IP` | empty | Public IP of the gateway, written into its ICE candidates so internet browsers can reach it. Required for internet-facing deployments. Empty means no rewrite (LAN or loopback only). FreeSWITCH also advertises it on the trunk profile unless `TRING_TRUNK_IP` is set. |
| `TRING_GATEWAY_ADDR` | `:8087` | The gateway's HTTP/WebSocket listen address. |
| `TRING_GATEWAY_UDP_MIN` / `TRING_GATEWAY_UDP_MAX` | `10000` / `10100` | UDP port range for browser media, about one port per call. The default caps you at roughly 100 concurrent calls. Open this range on the firewall. |
| `TRING_GATEWAY_REQUIRE_AUTH` | `true` | When `true`, the gateway will not start without Redis (see the note above). `/rtc` always requires a signed token regardless of this setting. Keep it `true`. |
| `TRING_MEDIA_GRACE_SECONDS` | `30` | How long a dropped participant's slot is held so a page refresh can rejoin the same call. See [page-refresh grace](/tring/features/web-calling/#page-refresh-grace). |
| `TRING_GATEWAY_DRAIN_SECONDS` | `300` | On `docker stop` (SIGTERM) the gateway stops taking new calls and waits up to this long for live calls to end before exiting. New web calls, inbound phone calls and two-number connects are refused meanwhile, and `/healthz` returns 503. Keep the gateway's `stop_grace_period` in `docker-compose.yml` (`330s`) above it, or Docker kills it mid-drain. |
| `TRING_RTC_DEV_TOKENS` | `false` | Mounts an **unauthenticated** token mint at `/rtc/dev-token` for the local test client. |
| `TRING_TRUSTED_PROXIES` | empty | Comma-separated CIDRs of the reverse proxies in front of the **api**. Only `X-Forwarded-For` hops inside these ranges are trusted when logging the client IP and rate limiting console login. Empty ignores `X-Forwarded-For` entirely. |
| `TRING_OUTBOUND_ALLOW_CIDRS` | empty | Comma-separated CIDRs that tenant-supplied URLs (webhooks, IVR `http_callback` nodes) may reach even though they aren't public. By default the **api** and **gateway** refuse to connect to loopback, private (`10/8`, `172.16/12`, `192.168/16`, `fc00::/7`), link-local (including cloud metadata `169.254.169.254`), CGNAT (`100.64/10`), multicast and reserved addresses, on every redirect too. List only your CRM's range, for example `10.20.0.0/16`. |

:::danger
- Never set `TRING_RTC_DEV_TOKENS=true` in production. Anyone could mint agent tokens.
- Set `TRING_TRUSTED_PROXIES` only to ranges you control (for example `127.0.0.1/32` for a proxy on the same host). A range that is too wide lets anyone in it forge client IPs in your logs.
- An entry in `TRING_OUTBOUND_ALLOW_CIDRS` overrides every block, loopback and metadata included. Listing `127.0.0.0/8` or `169.254.0.0/16` lets any tenant reach ESL, Redis, Postgres or your cloud credentials from a webhook or IVR flow.
:::

### Trunk and WhatsApp interfaces (FreeSWITCH)

| Variable | Default | What it does |
|---|---|---|
| `TRING_TRUNK_IP` | empty | IP the FreeSWITCH trunk (external) profile binds **and** advertises. Empty uses the primary interface and advertises `TRING_PUBLIC_IP`. Set it when the carrier reaches you on a private circuit on another interface. |
| `TRING_CARRIER_CIDRS` | empty | Comma-separated IPs/CIDRs your carriers send SIP from. When set, FreeSWITCH's trunk profile rejects inbound INVITEs from any other address with `403` (fake inbound calls, spoofed caller ID). Empty accepts any source. Restart the `freeswitch` container after changing it. |
| `TRING_SIP_RELAYS` | empty | Shares this server's carrier trunk with other Tring servers (a UAT box, a dedicated instance). `name=ipv4:port:numbers` entries separated by `;`, where `numbers` is a comma-separated list of E.164 numbers and `A-B` ranges (at most 1000 per relay). Calls to a relay's numbers are sent to that server; calls from that server's IP go out over the carrier, keeping their caller ID only if it is one of the relay's numbers (otherwise the relay's first number). Each relay's IP is added to the carrier ACL. Malformed entries are skipped and logged. See [Sharing a trunk with another server](#sharing-a-trunk-with-another-server). |
| `TRING_SIP_RELAY_GATEWAY` | empty | Gateway name of the carrier trunk that relays call out on (`t_…`, shown in the FreeSWITCH log and `sofia status`). Unset rejects relays' outbound calls. |
| `TRING_WHATSAPP_ENABLED` | `false` | Loads the TLS-only `whatsapp` SIP profile for inbound WhatsApp calls. Off binds nothing. Needs the certificates described in the [WhatsApp guide](/tring/guides/whatsapp-sip/). |
| `TRING_WHATSAPP_IP` | empty | IP the `whatsapp` profile binds. Empty uses the primary interface. |
| `TRING_WHATSAPP_PUBLIC_IP` | empty | IP advertised to Meta in SIP/SDP. Empty falls back to `TRING_PUBLIC_IP`, then the bind IP. |
| `TRING_WHATSAPP_TLS_PORT` | `5061` | TLS port Meta connects to (the port set on the business number). |

### Sharing a trunk with another server

When a second Tring server (UAT, or a dedicated instance for one client) can't have its own carrier connection, the server that holds the trunk relays calls for it:

```
TRING_SIP_RELAYS=uat=10.0.5.20:5080:+912265915001-+912265915030
TRING_SIP_RELAY_GATEWAY=t_4cc0a6963ea0
```

- **On the trunk server:** set the two variables above and restart the `freeswitch` container. Store the numbers in the format the carrier sends (for Tata, `+91…`).
- **On the relayed server:** add a trunk pointing at the trunk server's FreeSWITCH (`<ip>:5080`, IP authentication), set `TRING_CARRIER_CIDRS` to that IP, and add the relayed numbers as inbound numbers.
- **Network:** the two servers must reach each other on UDP `5080` and the RTP range. Open them between those two hosts only. The trunk server stays in the audio path.

A relay's IP can place calls over your carrier, so only list hosts you control.

### Inbound phone calls (SIP plug)

The SIP plug is a SIP endpoint inside the gateway. FreeSWITCH bridges inbound carrier calls to it, and each call then runs the same IVR and routing as a web call. It also places outbound calls that the IVR or an agent dials. It is **off** by default; turn it on once a trunk is live.

| Variable | Default | What it does |
|---|---|---|
| `TRING_FSPLUG_ENABLED` | `false` | Turns the SIP plug on. Needs the gateway's database connection to map dialed numbers to flows. |
| `TRING_FSPLUG_SIP_LISTEN` | `:5090` | The plug's SIP bind address. Must not be `:5080` (FreeSWITCH's trunk profile). The port must equal `TRING_GATEWAY_SIP_PORT`. |
| `TRING_FSPLUG_SIP_ADVERTISE` | `127.0.0.1` | IP the plug advertises. Must equal `TRING_GATEWAY_ADVERT_IP`. |
| `TRING_FSPLUG_RTP_MIN` / `TRING_FSPLUG_RTP_MAX` | `12000` / `12100` | UDP range for the FreeSWITCH-to-gateway audio leg (separate from the browser range). Open it on the host firewall. |
| `TRING_GATEWAY_ADVERT_IP` | `127.0.0.1` | Read by FreeSWITCH: where it sends inbound carrier calls. Keep equal to `TRING_FSPLUG_SIP_ADVERTISE`. |
| `TRING_GATEWAY_SIP_PORT` | `5090` | Read by FreeSWITCH: the plug's port. Keep equal to the port in `TRING_FSPLUG_SIP_LISTEN`. |

:::caution
If the FreeSWITCH pair (`TRING_GATEWAY_ADVERT_IP`, `TRING_GATEWAY_SIP_PORT`) and the gateway pair (`TRING_FSPLUG_SIP_ADVERTISE`, `TRING_FSPLUG_SIP_LISTEN`) disagree, inbound calls never reach the gateway.
:::

## 6. Call recording storage

Recording and its sample rate are per-tenant settings (see [Recording and call logs](/tring/features/recording-and-call-logs/)). These variables decide where files go.

| Variable | Default | What it does |
|---|---|---|
| `TRING_RECORDINGS_DIR` | `/var/lib/tring/recordings` | Local spool shared by the gateway (writes) and the api (serves). Compose bind-mounts `./recordings` here. |
| `TRING_S3_BUCKET` | empty | When set, finished recordings are uploaded to this bucket and the local copy is deleted. Empty keeps recordings local only. |
| `TRING_S3_REGION` | empty | Bucket region. |
| `TRING_S3_ENDPOINT` | empty | Empty for AWS S3. Set to your host for an S3-compatible store (MinIO, Cloudflare R2). |
| `TRING_S3_ACCESS_KEY` / `TRING_S3_SECRET_KEY` | empty | Static credentials. Leave **both** empty on AWS to use the instance's IAM role. |
| `TRING_S3_PREFIX` | `recordings` | Key prefix. Objects are stored as `<prefix>/<tenant-id>/YYYY/MM/DD/<leg-id>.<format>`. |
| `TRING_S3_USE_SSL` | `true` | Use HTTPS to the S3 endpoint. Keep `true`. |

:::tip[Least privilege for S3]
Recordings are personal data. Prefer an IAM role over static keys, scope it to `s3:PutObject` and `s3:GetObject` on this bucket and prefix only, keep the bucket private (Block Public Access on), and turn on default encryption and access logging.
:::

## 6b. IVR audio library and text-to-speech

| Variable | Default | What it does |
|---|---|---|
| `TRING_AUDIO_DIR` | `/var/lib/tring/sounds` | Where the api writes uploaded audio and synthesized prompts, and where the gateway reads them for playback. Shared mount. TTS output goes to `<audio_dir>/tts`. |
| `TRING_TTS_PIPER_BIN` | `/opt/piper/piper` | Piper binary. |
| `TRING_TTS_PIPER_MODEL` | `/usr/local/share/piper/voice.onnx` | Piper voice model. Its `.onnx.json` must sit next to it. |
| `TRING_TTS_VOICE` | empty | Speaker id for multi-speaker models (empty = speaker 0). |

Piper, a default English voice and ffmpeg ship in the api image, so TTS needs no configuration. If the binary, the model or ffmpeg is missing, TTS turns itself off and text-only prompts stay silent. See [Sounds and hold music](/tring/features/sounds-and-hold-music/).

## 7. WebRTC ICE (STUN and TURN)

STUN is off on purpose: the gateway is the public endpoint and learns each browser's address itself. TURN is the fallback for networks that block UDP.

| Variable | Default | What it does |
|---|---|---|
| `TRING_STUN_URL` | empty | Optional STUN server. Only for unusual topologies. |
| `TRING_TURN_URL` | empty | TURN server URL, for example `turns:turn.example.com:443`. |
| `TRING_TURN_USERNAME` / `TRING_TURN_PASSWORD` | empty | Static TURN credentials, handed to every client. Only for closed dev setups. Ignored when `TRING_TURN_SECRET` is set. |
| `TRING_TURN_SECRET` | empty | Shared secret for coturn's REST API (`use-auth-secret`, `static-auth-secret=<secret>`). The api mints short-lived TURN credentials per token request; the secret never reaches clients. Credential lifetime is the tenant's max call duration plus 5 minutes. |
| `TRING_TURN_RELAY_ONLY` | `false` | Force all gateway media through TURN. |

In production use `TRING_TURN_SECRET` (ephemeral credentials) with TURNS on port 443, and never ship static TURN credentials in a mobile app.

## 8. Image tag

| Variable | Default | What it does |
|---|---|---|
| `TRING_TAG` | `develop` | Docker image tag for `tring-api`, `tring-gateway` and `tring-freeswitch`. `develop` tracks the latest `main`. Pin a release tag in production. |

## Other variables

These are not in `.env.example` but are read by the binaries, Compose or FreeSWITCH. Most deployments never need them.

| Variable | Default | What it does |
|---|---|---|
| `TRING_API_ADDR` | `:8085` | The api's listen address (also `--addr`). |
| `TRING_TURN_TTL_SECONDS` | `14700` | Lifetime of the TURN credential the gateway mints for its own relay candidate. Must outlive the longest call. |
| `TRING_AGENT_TRANSPORT` | `gateway` | How the api builds an agent's call leg for outbound calls. `gateway` is the supported mode; `sip` is legacy. |
| `TRING_AGENT_GATEWAY` | `tring-gw` | FreeSWITCH gateway name that points at the Tring gateway. |
| `TRING_AGENT_JITTER_MSEC` | `60:200` | FreeSWITCH jitter buffer on the agent leg (`min:max` ms). Empty disables it. |
| `TRING_GATEWAY_DIR` | `/etc/freeswitch/sip_profiles/external/dynamic` | Where the api writes trunk gateway XML. Must be the directory FreeSWITCH includes. |
| `TRING_INTERNAL_IP` | primary interface | FreeSWITCH: IP of the private profile the gateway talks to. `127.0.0.1` for single-machine dev. |
| `TRING_EXTERNAL_SIP_PORT` | `5080` | FreeSWITCH: trunk profile SIP port. Set to the port your carrier signals on. |
| `TRING_INTERNAL_SIP_PORT` | `5060` | FreeSWITCH: private profile SIP port. |
| `TRING_RTP_START_PORT` / `TRING_RTP_END_PORT` | `16384` / `32768` | FreeSWITCH RTP range. Narrow it on a VPS and open only that range. |
| `TRING_API_UID` / `TRING_API_GID` | `1000` / `1000` | Compose: user the api and gateway containers run as, so they can write the bind-mounted directories. |

## Production checklist

- Replace every secret: `TRING_JWT_SECRET`, `TRING_FS_BASIC_AUTH`, `TRING_ESL_PASSWORD`, `TRING_ADMIN_PASSWORD` (before first boot). `make .env` / `install.sh` generate these. The Postgres password in `TRING_DSN` is not generated; the compose file publishes Postgres on `127.0.0.1` only.
- Set `TRING_SECRETS_KEY` so SIP passwords are encrypted at rest, and back it up outside the database backups. The api logs a warning on every start while it is empty.
- Serve the api and gateway over HTTPS behind a TLS proxy; set `TRING_GATEWAY_PUBLIC_URL=https://…` and `TRING_TRUSTED_PROXIES` to the proxy's address.
- Set `TRING_PUBLIC_IP`, widen `TRING_GATEWAY_UDP_*` for your load, and open only the media ranges you use.
- Keep `TRING_GATEWAY_REQUIRE_AUTH=true` and `TRING_RTC_DEV_TOKENS=false`.
- Keep ESL (`8021`), Postgres and Redis off the public internet.
- Set `TRING_CARRIER_CIDRS` to your carriers' signalling addresses.
- Set `TRING_REDIS_PASSWORD` (generated by `make .env` / `install.sh` for new installs; add it by hand to older `.env` files) unless Redis runs on an isolated network.
- Use ephemeral TURN (`TRING_TURN_SECRET`) and TURNS on 443.
- Store recordings in a private, encrypted bucket with an IAM role.
- Pin `TRING_TAG` to a release.
- Turn on MFA for your cloud and registry accounts, keep audit logging on, and rotate secrets on a schedule (except `TRING_SECRETS_KEY`, which can't be rotated yet).
