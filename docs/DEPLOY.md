# Deploying Tring

A v0.1 deployment is **one `docker compose up -d`** on any host with Docker. Everything is containerized: API, orchestrator, FreeSWITCH, Postgres. No host-side Go toolchain, no manual ESL module loads.

## What you need on the host

- Docker Engine 24+ and Compose v2
- A public IP that you can put on your SIP provider's allowlist (Exotel/Airtel/Jio/Tata all require this)
- UDP ports `16384–32768` open inbound — RTP audio
- TCP ports `5060`, `5066`, `7443`, `8021` open inbound (or whichever subset your use case needs)
- ~1 GB RAM, single CPU. The Go binaries are ~15 MB each (distroless static).

A `$5/month` Hetzner/DigitalOcean VPS handles this comfortably.

## First deploy

```bash
git clone https://github.com/shridarpatil/tring && cd tring
cp .env.example .env
# Edit .env: trunk creds, JWT secret, FS basic auth. See .env.example for each var.
docker compose up -d
docker compose logs -f
```

The first `compose up` builds both Go images locally from the included Dockerfile (~30 seconds with deps cached). Subsequent ups reuse the images.

## What runs where

| Container | Image | Network | Purpose |
|---|---|---|---|
| `tring-api` | `tring-api:local` (built from `Dockerfile`) | host | REST API on `:8085` |
| `tring-orchestrator` | `tring-orchestrator:local` (same Dockerfile, different build arg) | host | ESL event subscriber, no exposed port |
| `tring-fs` | `safarov/freeswitch:latest` | host | SIP/RTP/ESL/WSS |
| `tring-db` | `postgres:16` | bridge | Postgres on `:5434` |

The Go services use **host networking** so they reach FreeSWITCH on `127.0.0.1:8021` without bridge-DNS gymnastics. Postgres stays on a bridge network and is reached on the mapped host port `:5434`.

## Configuration

All four services read from `.env` at the repo root. See `.env.example` for the full schema. The defaults are dev-safe (placeholder values, registers disabled). Production needs at minimum:

- `EXOTEL_*` (or `AIRTEL_*` / `JIO_*` / `TATA_*`) credentials filled in
- `<PROV>_REGISTER=true` for whichever trunks you're activating
- `TRING_JWT_SECRET` rotated off the default
- `TRING_FS_BASIC_AUTH` rotated off the default (must match the value in `freeswitch/conf/autoload_configs/xml_curl.conf.xml`'s `gateway-credentials` param)

## Putting TLS in front of it (for prod agent browsers)

The API runs plain HTTP on `:8085`. FreeSWITCH's WSS is on `:7443` with a self-signed cert. For browser agents working from anywhere outside your trusted LAN, both need real certificates.

Recommended: drop a Caddy or Traefik container into `docker-compose.yml` that handles Let's Encrypt for `api.your-domain` (proxying to `:8085`) and `voice.your-domain` (proxying to `:7443`). Override `TRING_WS_URL=wss://voice.your-domain:7443` in `.env` so the SDK gets the right WSS URL in `/v1/sdk/credentials`.

This isn't built into the default compose because the right TLS posture depends on your domain / cert setup.

## Verifying the deploy

```bash
# API up?
curl -sS http://localhost:8085/healthz   # → {"status":"ok"}

# DB seeded?
docker exec tring-db psql -U tring -d tring -c \
  "SELECT name, sip_domain FROM tenants;"

# Orchestrator subscribed to ESL?
docker compose logs tring-orchestrator | grep "subscribed to events"

# FreeSWITCH gateway state (real test of trunk creds)
docker exec tring-fs fs_cli -x "sofia status profile external"
```

If the trunk shows as `REGED`, you're live. If `NOREG`:

- Check `docker logs tring-fs | grep -i <provider>` for the SIP REGISTER response cause.
- Common: wrong realm, wrong password, or your host IP isn't on the provider's allowlist.

## Updating

```bash
git pull
docker compose build api orchestrator   # rebuild Go images
docker compose up -d                    # rolling restart, picks up new images
```

The first rebuild after a fresh `git pull` takes ~30s; subsequent rebuilds are 2-3s thanks to BuildKit cache mounts in the Dockerfile.

## Backups

- Postgres: the `tring-db-data` named volume holds all CDRs, tenant config, webhook history. Back up with `docker exec tring-db pg_dump -U tring tring > backup.sql`.
- FreeSWITCH: stateless; configs live in the repo, sounds in a regenerable volume. No backup needed.

## Tearing down

```bash
docker compose down            # stop containers, keep volumes
docker compose down -v         # also delete Postgres data (CDRs gone)
```
