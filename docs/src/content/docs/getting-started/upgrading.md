---
title: Upgrading
description: How to upgrade a Docker Compose install, and the one-time switch to the single tring image.
---

Set `TRING_TAG` in `.env` to the release you want, then:

```bash
docker compose pull
docker compose up -d
```

The `tring` container drains on restart: it stops taking new calls and lets live ones finish, for up to `TRING_GATEWAY_DRAIN_SECONDS` (300 by default). Upgrade outside call hours when you can.

## One-time: the single `tring` image

From this release Tring ships as one image, `shridh0r/tring`, which runs both the api role (REST API, console) and the gateway role (call plane). The old `tring-api` and `tring-gateway` images get no new versions.

If you track `:develop`, `docker compose pull` with the old `docker-compose.yml` still succeeds but keeps the old version, with no error. (With a pinned release tag the old images don't exist, so the pull fails loudly.) You have to switch to the new `docker-compose.yml` once:

1. Back up `docker-compose.yml`.
2. Fetch the new `docker-compose.yml` for the version you're moving to. Either run the installer with that version, `TRING_VERSION=<new tag> sh install.sh` (`develop` for `:develop`), or copy `docker-compose.yml` from the repository at that tag. Running `install.sh` without `TRING_VERSION` re-fetches the version already in `.env`, which is the old compose.
3. Set `TRING_TAG` in `.env` to the same version.
4. Outside call hours, run:

   ```bash
   docker compose pull
   docker compose up -d --remove-orphans
   ```

   `--remove-orphans` stops the old `tring-api` and `tring-gateway` containers. Without it they keep running and the new `tring` container can't bind `:8085`, `:8087` and `:8089`.
5. Check that `curl -s localhost:8087/healthz` returns 200, then place one web call and one phone call.

Apart from `TRING_TAG`, your `.env` needs no changes. Logs are now in one place: `docker logs -f tring`.

One behaviour change: the api and the call plane now restart together, so a deploy that only changes the api also drains live calls.
