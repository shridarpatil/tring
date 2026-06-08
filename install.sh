#!/bin/sh
# Tring zero-clone installer.
#
# Drops the minimum needed to run the PRE-BUILT images with
# `docker compose up -d` into the current directory:
#   - docker-compose.yml   (pulls ghcr.io/shridarpatil/tring-* images)
#   - .env.example         (copy to .env to override defaults)
#   - freeswitch/sounds/                         (host dir: custom sounds / hold music)
#   - freeswitch/conf/sip_profiles/external/dynamic/  (host dir: UI-managed trunk gateways)
#
# The FreeSWITCH config (vars.xml, dialplan, sofia profiles) is BAKED INTO the
# tring-freeswitch image — it is no longer bind-mounted, so it is not downloaded
# here. Editing FS config means rebuilding that image, not editing host files.
#
# Idempotent — safe to re-run.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/shridarpatil/tring/main/install.sh | sh
#   cd tring
#   cp .env.example .env     # edit network IPs, TZ, admin password, TRING_TAG
#   docker compose pull && docker compose up -d
#
# Override the target dir:
#   TRING_DIR=/srv/tring curl -fsSL .../install.sh | sh

set -eu

REPO_RAW="${TRING_REPO:-https://raw.githubusercontent.com/shridarpatil/tring/main}"
TARGET="${TRING_DIR:-tring}"

# The two files to fetch. Everything else (the four service images + the baked
# FreeSWITCH config) comes from the published images via `docker compose pull`.
FILES="
docker-compose.yml
.env.example
"

# The host directories docker-compose.yml bind-mounts (shared api<->freeswitch).
# Created empty; Tring fills them at runtime (sound uploads, dynamic trunk XML).
DIRS="
freeswitch/sounds
freeswitch/conf/sip_profiles/external/dynamic
"

# Pick a downloader. curl on macOS/Linux, wget as fallback.
if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -q "$1" -O "$2"; }
else
    echo "error: need curl or wget on PATH" >&2
    exit 1
fi

mkdir -p "$TARGET"
cd "$TARGET"

echo "tring: downloading into $(pwd)"
for f in $FILES; do
    mkdir -p "$(dirname "$f")"
    echo "  $f"
    fetch "$REPO_RAW/$f" "$f"
done

for d in $DIRS; do
    mkdir -p "$d"
done

cat <<EOF

tring: ready in $(pwd)

Next steps:
  cd $TARGET
  cp .env.example .env

  # Edit .env — for a SERVER (not localhost) the network IPs are what people
  # get wrong (see section 4 of .env.example and docs/EDGE_DEPLOYMENT.md):
  #   TRING_INTERNAL_IP=            # empty -> FreeSWITCH binds the host/LAN IP
  #   TRING_GATEWAY_ADVERT_IP=<server-ip reachable by FS + browsers>
  #   TRING_GATEWAY_PUBLIC_URL=https://gw.your-domain   # browser-reachable
  #   TRING_PUBLIC_IP=<public ip>   # only needed once a real SIP trunk is wired
  #   TZ=Asia/Kolkata               # applies to every container
  #   TRING_ADMIN_PASSWORD=...       # and JWT/FS secrets
  #   TRING_TAG=develop              # pin a release (e.g. v0.1.0) for production

  docker compose pull        # pull the four ghcr.io/shridarpatil/tring-* images
  docker compose up -d

Then open http://<server>:8085/admin/ and sign in with the
TRING_ADMIN_EMAIL / TRING_ADMIN_PASSWORD from .env.
EOF
