#!/bin/sh
# Tring zero-clone installer.
#
# Drops the minimum file set needed for `docker compose up -d` into the
# current directory:
#   - docker-compose.yml
#   - .env.example  (rename to .env to override defaults)
#   - freeswitch/conf/*  (vars.xml + dialplan + sofia profiles)
#   - freeswitch/conf/sip_profiles/external/dynamic/  (empty dir, RW
#     mount shared by api↔fs for UI-managed trunk gateways)
#
# Idempotent — safe to re-run, will only overwrite known-tracked files
# and never touches anything outside the directories it creates.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/shridarpatil/tring/main/install.sh | sh
#   # then:
#   cd tring && docker compose up -d
#
# Override the target dir:
#   TRING_DIR=/srv/tring curl -fsSL .../install.sh | sh

set -eu

REPO_RAW="${TRING_REPO:-https://raw.githubusercontent.com/shridarpatil/tring/main}"
TARGET="${TRING_DIR:-tring}"

# The minimal file list. Keep this in sync with the bind mounts in
# docker-compose.yml — if a new FS config gets mounted from host, add
# it here too.
FILES="
docker-compose.yml
.env.example
freeswitch/conf/vars.xml
freeswitch/conf/dialplan/default.xml
freeswitch/conf/directory/default.xml
freeswitch/conf/autoload_configs/modules.conf.xml
freeswitch/conf/autoload_configs/acl.conf.xml
freeswitch/conf/autoload_configs/xml_curl.conf.xml
freeswitch/conf/autoload_configs/switch.conf.xml
freeswitch/conf/sip_profiles/external.xml
freeswitch/conf/sip_profiles/external/tring-gw.xml
freeswitch/conf/sip_profiles/internal.xml
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

echo "tring: downloading minimum file set into $(pwd)"
for f in $FILES; do
    mkdir -p "$(dirname "$f")"
    echo "  $f"
    fetch "$REPO_RAW/$f" "$f"
done

# Dynamic gateway dir — api writes t_<uuid>.xml here, fs reads. Both
# containers bind-mount it; the operator never edits files in here.
mkdir -p freeswitch/conf/sip_profiles/external/dynamic

cat <<EOF

tring: ready in $(pwd)

Next steps:
  cd $TARGET
  cp .env.example .env        # then edit TRING_ADMIN_PASSWORD etc.
  docker compose up -d

Then open http://localhost:8085/admin/ and sign in with the
TRING_ADMIN_EMAIL / TRING_ADMIN_PASSWORD you set in .env (defaults:
admin@tring.local / tring_dev_change_me).
EOF
