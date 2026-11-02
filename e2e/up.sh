#!/bin/sh
# Bring up the real Tring stack for the e2e tests: api, gateway and FreeSWITCH
# built from this checkout, plus Postgres and Redis. Writes .env on first run
# (kept on re-runs, so the bootstrap admin's password stays valid) and the
# test's settings to .e2e.env (sourced locally; appended to $GITHUB_ENV in CI).
#
#   e2e/up.sh                       build and start
#   E2E_SKIP_BUILD=1 e2e/up.sh      start images already built (CI builds them
#                                   with the GHA cache first)
#
# Ports default to the stock ones. On a box where they're taken:
#   TRING_E2E_API_PORT (8085), TRING_E2E_REDIS_PORT (6379).
set -eu
cd "$(dirname "$0")/.."

API_PORT=${TRING_E2E_API_PORT:-8085}
REDIS_PORT=${TRING_E2E_REDIS_PORT:-6379}
export TRING_TAG=${TRING_TAG:-e2e} # never clobber locally pulled :develop images

if [ ! -f .env ]; then
	rand() { od -An -tx1 -N24 /dev/urandom | tr -d ' \n'; }
	key32() { od -An -tx1 -N32 /dev/urandom | tr -d ' \n'; }
	sed -e "s|^TRING_JWT_SECRET=.*|TRING_JWT_SECRET=$(rand)$(rand)|" \
		-e "s|^TRING_SECRETS_KEY=.*|TRING_SECRETS_KEY=$(key32)|" \
		-e "s|^TRING_FS_BASIC_AUTH=.*|TRING_FS_BASIC_AUTH=fs:$(rand)|" \
		-e "s|^TRING_ESL_PASSWORD=.*|TRING_ESL_PASSWORD=$(rand)|" \
		-e "s|^TRING_REDIS_PASSWORD=.*|TRING_REDIS_PASSWORD=$(rand)|" \
		-e "s|^TRING_ADMIN_PASSWORD=.*|TRING_ADMIN_PASSWORD=$(rand)|" \
		-e "s|^TRING_REDIS_ADDR=.*|TRING_REDIS_ADDR=127.0.0.1:$REDIS_PORT|" \
		-e "s|^TRING_GATEWAY_PUBLIC_URL=.*|TRING_GATEWAY_PUBLIC_URL=http://127.0.0.1:8087|" \
		.env.example >.env
	cat >>.env <<EOF

# --- e2e (e2e/up.sh) ---
TRING_API_ADDR=:$API_PORT
TRING_FSPLUG_ENABLED=true
TRING_FSPLUG_SIP_ADVERTISE=127.0.0.1
TRING_INTERNAL_IP=127.0.0.1
TRING_API_UID=$(id -u)
TRING_API_GID=$(id -g)
EOF
	chmod 600 .env
fi
get() { sed -n "s/^$1=//p" .env | tail -n 1; }

compose="docker compose"
if [ "$REDIS_PORT" != 6379 ]; then
	cat >.e2e.compose.yml <<EOF
services:
  redis:
    ports: !override
      - "127.0.0.1:$REDIS_PORT:6379"
EOF
	compose="docker compose -f docker-compose.yml -f .e2e.compose.yml"
fi

if [ -z "${E2E_SKIP_BUILD:-}" ]; then
	$compose build api gateway freeswitch
fi
$compose up -d postgres redis freeswitch api gateway

# wait <what> <seconds> <command...>: poll until the command succeeds.
wait_for() {
	what=$1 secs=$2
	shift 2
	end=$(($(date +%s) + secs))
	until "$@" >/dev/null 2>&1; do
		if [ "$(date +%s)" -ge "$end" ]; then
			echo "e2e/up.sh: timed out after ${secs}s waiting for $what" >&2
			$compose logs --tail=50 api gateway freeswitch >&2 || true
			exit 1
		fi
		sleep 1
	done
	echo "e2e/up.sh: $what ready"
}
esl_pw=$(get TRING_ESL_PASSWORD)
fs_cli() { docker exec tring-fs fs_cli -p "$esl_pw" -x "$1"; }
wait_for api 120 curl -sf "http://127.0.0.1:$API_PORT/healthz"
wait_for gateway 60 curl -sf http://127.0.0.1:8087/healthz
wait_for freeswitch 120 sh -c "docker exec tring-fs fs_cli -p '$esl_pw' -x status | grep -q '^UP'"

trunk_ip=$(fs_cli 'global_getvar tring_trunk_ip' | tr -d '\r\n')
ext_port=$(fs_cli 'global_getvar external_sip_port' | tr -d '\r\n')
cat >.e2e.env <<EOF
export TRING_E2E=1
export TRING_E2E_API_PORT=$API_PORT
export TRING_E2E_ADMIN_EMAIL=$(get TRING_ADMIN_EMAIL)
export TRING_E2E_ADMIN_PASSWORD=$(get TRING_ADMIN_PASSWORD)
export TRING_E2E_TRUNK_IP=$trunk_ip
export TRING_E2E_FS_EXTERNAL=$trunk_ip:$ext_port
EOF
if [ -n "${GITHUB_ENV:-}" ]; then
	sed 's/^export //' .e2e.env >>"$GITHUB_ENV"
fi
echo "e2e/up.sh: stack up; settings in .e2e.env"
