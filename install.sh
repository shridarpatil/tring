#!/bin/sh
# Tring zero-clone installer.
#
# Drops the minimum needed to run the PRE-BUILT images with
# `docker compose up -d` into the current directory:
#   - docker-compose.yml   (pulls the shridh0r/tring-* images from Docker Hub)
#   - .env.example         (copy to .env to override defaults)
#   - freeswitch/sounds/                         (host dir: prompt audio / hold music, shared api<->gateway)
#   - freeswitch/conf/sip_profiles/external/dynamic/  (host dir: UI-managed trunk gateways)
#   - recordings/                                (host dir: stereo call recordings the gateway writes + api serves)
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
#   $EDITOR .env             # generated with random secrets; set network IPs, TZ, TRING_TAG
#   docker compose pull && docker compose up -d
#
# Override the target dir:
#   TRING_DIR=/srv/tring curl -fsSL .../install.sh | sh

set -eu

REPO_RAW="${TRING_REPO:-https://raw.githubusercontent.com/shridarpatil/tring/main}"
TARGET="${TRING_DIR:-tring}"

# The two files to fetch. Everything else — the three tring images
# (shridh0r/tring-{api,gateway,freeswitch}), the upstream Postgres + Redis
# images, and the baked FreeSWITCH config — comes via `docker compose pull`.
FILES="
docker-compose.yml
.env.example
"

# The host directories docker-compose.yml bind-mounts. Created empty (owned by
# this user, so the non-root gateway can write); Tring fills them at runtime
# (sound uploads, dynamic trunk XML, call recordings).
DIRS="
freeswitch/sounds
freeswitch/conf/sip_profiles/external/dynamic
recordings
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

# First run only: create .env with fresh random secrets. The api and gateway
# refuse to start while the .env.example placeholders are still in place.
rand() { od -An -tx1 -N24 /dev/urandom | tr -d ' \n'; }
key32() { od -An -tx1 -N32 /dev/urandom | tr -d ' \n'; }
if [ ! -f .env ]; then
    sed -e "s|^TRING_JWT_SECRET=.*|TRING_JWT_SECRET=$(rand)$(rand)|" \
        -e "s|^TRING_SECRETS_KEY=.*|TRING_SECRETS_KEY=$(key32)|" \
        -e "s|^TRING_FS_BASIC_AUTH=.*|TRING_FS_BASIC_AUTH=fs:$(rand)|" \
        -e "s|^TRING_ESL_PASSWORD=.*|TRING_ESL_PASSWORD=$(rand)|" \
        -e "s|^TRING_REDIS_PASSWORD=.*|TRING_REDIS_PASSWORD=$(rand)|" \
        -e "s|^TRING_ADMIN_PASSWORD=.*|TRING_ADMIN_PASSWORD=$(rand)|" \
        .env.example > .env
    chmod 600 .env
    echo "  .env (random secrets generated)"
fi

cat <<EOF

tring: ready in $(pwd)

Next steps:
  cd $TARGET

  # .env was generated with random secrets (admin password included).
  # BACK UP .env: TRING_SECRETS_KEY encrypts the stored SIP trunk passwords;
  # without it they can't be recovered and must be re-entered.
  # Edit it — for a SERVER (not localhost) the network IPs are what people
  # get wrong (see section 5 of .env.example and docs/src/content/docs/architecture/state-and-ha.md):
  #   TRING_GATEWAY_PUBLIC_URL=https://gw.your-domain  # browser-reachable, must hit /rtc
  #   TRING_PUBLIC_IP=<public/elastic ip>   # rewrites the gateway's WebRTC ICE candidate
  #   TRING_REDIS_ADDR=127.0.0.1:6379       # gateway HARD-dep when REQUIRE_AUTH=true (default)
  #   TZ=Asia/Kolkata                       # applies to every container
  #   TRING_TAG=develop                      # pin a release (e.g. v0.1.0) for production
  docker compose pull        # pull the shridh0r/tring-* images + Postgres/Redis
  docker compose up -d

Then open http://<server>:8085/admin/ and sign in with the
TRING_ADMIN_EMAIL / TRING_ADMIN_PASSWORD from .env.
EOF
