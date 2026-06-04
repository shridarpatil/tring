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

## Putting TLS in front of it (for prod browsers)

Browsers reach two HTTP surfaces: the **media gateway** signaling (`:8087` — `/webrtc/offer` + `/agent/ws`) and, if you expose it, the API (`:8085`). Both run plain HTTP by default; browsers outside your trusted LAN need real certificates in front of them. (FreeSWITCH is private and never internet-exposed — there's no browser-facing WSS anymore; the gateway replaced it in the M5 cutover.)

Recommended: drop a Caddy or Traefik container into `docker-compose.yml` that handles Let's Encrypt for `gw.your-domain` (proxying to the gateway `:8087`, HTTPS + WSS) and, if needed, `api.your-domain` (proxying to `:8085`). Set `TRING_GATEWAY_PUBLIC_URL=https://gw.your-domain` in `.env` so the API hands browsers the right gateway URL. The gateway's UDP media range must also be reachable on `TRING_PUBLIC_IP` (see `docs/EDGE_DEPLOYMENT.md`).

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

### From source

```bash
git pull
docker compose build api orchestrator   # rebuild Go images
docker compose up -d                    # rolling restart, picks up new images
```

The first rebuild after a fresh `git pull` takes ~30s; subsequent rebuilds are 2-3s thanks to BuildKit cache mounts in the Dockerfile.

### From pre-built images (CI publishes both registries)

Every push to `main` publishes `:develop` tags to **GHCR** and **Docker Hub**:

- `ghcr.io/shridarpatil/tring-api:develop` / `:tring-orchestrator:develop`
- `docker.io/shridarpatil/tring-api:develop` / `:tring-orchestrator:develop`

Tag pushes (`v0.2.0`) publish `:v0.2.0` *and* `:0.2.0` (semver-stripped). Multi-arch (`linux/amd64` + `linux/arm64`).

To deploy from pre-built images, override the local-build directive in `docker-compose.yml`:

```yaml
services:
  api:
    image: ghcr.io/shridarpatil/tring-api:develop    # or a pinned version
    # build: { ... }       # remove or comment out the build block
  orchestrator:
    image: ghcr.io/shridarpatil/tring-orchestrator:develop
```

Then `docker compose pull && docker compose up -d` to roll forward.

### CI secrets (one-time setup)

The workflow always tries to log in to both registries. Add these repo secrets at **Settings → Secrets and variables → Actions** before the first push:

- `DOCKERHUB_USERNAME` — your Docker Hub login (often the same as your GitHub handle)
- `DOCKERHUB_PASSWORD` — a Docker Hub PAT with Read+Write on the `tring-*` repos

GHCR uses the workflow's built-in `GITHUB_TOKEN` — nothing to configure.

If you don't want Docker Hub mirroring, delete the two Docker Hub steps from `.github/workflows/build-images.yml` and the second `tags:` line in the build step.

## Backups

- Postgres: the `tring-db-data` named volume holds all CDRs, tenant config, webhook history. Back up with `docker exec tring-db pg_dump -U tring tring > backup.sql`.
- FreeSWITCH: stateless; configs live in the repo, sounds in a regenerable volume. No backup needed.

## Tearing down

```bash
docker compose down            # stop containers, keep volumes
docker compose down -v         # also delete Postgres data (CDRs gone)
```
