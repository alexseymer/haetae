#!/bin/sh
# Install or upgrade a self-hosted Haetae control plane with Docker Compose.
# Usage: curl -fsSL <url> | sh
#    or: sh deploy/install-server.sh
set -eu

HAETAE_DIR=${HAETAE_DIR:-/opt/haetae}
HAETAE_IMAGE=${HAETAE_IMAGE:-ghcr.io/alexseymer/haetae-server:latest}
HAETAE_WEB_PORT=${HAETAE_WEB_PORT:-8080}
HAETAE_GATEWAY_PORT=${HAETAE_GATEWAY_PORT:-8443}
COMPOSE_PROJECT_NAME=${COMPOSE_PROJECT_NAME:-haetae}
HAETAE_NO_PULL=${HAETAE_NO_PULL:-}

if [ -z "${HAETAE_HOST:-}" ]; then
	HAETAE_HOST=$(hostname -I 2>/dev/null | awk '{print $1}')
fi
if [ -z "${HAETAE_HOST:-}" ]; then
	HAETAE_HOST=$(hostname)
fi

die() {
	printf '%s\n' "$*" >&2
	exit 1
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "haetae: '$1' is required but was not found"
}

port_in_use() {
	# Prefer ss (no /dev/tcp). If ss is missing, skip the check.
	if ! command -v ss >/dev/null 2>&1; then
		return 1
	fi
	ss -ltnH "sport = :$1" 2>/dev/null | grep -q .
}

need_cmd docker
docker compose version >/dev/null 2>&1 || die "haetae: 'docker compose' (Compose v2) is required"

export COMPOSE_PROJECT_NAME
compose() {
	docker compose --project-directory "$HAETAE_DIR" -f "$HAETAE_DIR/compose.yaml" "$@"
}

# Refuse foreign listeners; our own stack may already hold the ports on upgrade.
already=
if [ -f "$HAETAE_DIR/compose.yaml" ]; then
	already=$(compose ps -q 2>/dev/null || true)
fi
if [ -z "$already" ]; then
	if port_in_use "$HAETAE_WEB_PORT"; then
		die "haetae: port $HAETAE_WEB_PORT is already in use"
	fi
	if port_in_use "$HAETAE_GATEWAY_PORT"; then
		die "haetae: port $HAETAE_GATEWAY_PORT is already in use"
	fi
fi

mkdir -p "$HAETAE_DIR"

# Keep the heredoc identical to deploy/compose.yaml (tested).
cat >"$HAETAE_DIR/compose.yaml" <<'COMPOSE'
services:
  db:
    image: postgres:17-alpine
    environment:
      POSTGRES_PASSWORD: ${HAETAE_DB_PASSWORD:?HAETAE_DB_PASSWORD is required}
      POSTGRES_DB: haetae
      POSTGRES_USER: haetae
    volumes:
      - db:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U haetae -d haetae"]
      interval: 5s
      timeout: 5s
      retries: 10
    restart: unless-stopped
    cap_drop:
      - ALL
    cap_add:
      - CHOWN
      - SETUID
      - SETGID
      - DAC_OVERRIDE
      - FOWNER
    security_opt:
      - no-new-privileges:true
  server:
    image: ${HAETAE_IMAGE:?HAETAE_IMAGE is required}
    depends_on:
      db:
        condition: service_healthy
    restart: unless-stopped
    environment:
      HAETAE_DATABASE_URL: postgres://haetae:${HAETAE_DB_PASSWORD:?HAETAE_DB_PASSWORD is required}@db:5432/haetae?sslmode=disable
      HAETAE_WEB_ADDR: ":8080"
      HAETAE_GATEWAY_ADDR: ":8443"
      HAETAE_WEB_URL: https://${HAETAE_HOST:?HAETAE_HOST is required}:${HAETAE_WEB_PORT:?HAETAE_WEB_PORT is required}
      HAETAE_GATEWAY_URL: https://${HAETAE_HOST:?HAETAE_HOST is required}:${HAETAE_GATEWAY_PORT:?HAETAE_GATEWAY_PORT is required}
      HAETAE_GATEWAY_NAMES: ${HAETAE_HOST:?HAETAE_HOST is required}
    ports:
      - "${HAETAE_WEB_PORT:?HAETAE_WEB_PORT is required}:8080"
      - "${HAETAE_GATEWAY_PORT:?HAETAE_GATEWAY_PORT is required}:8443"
    volumes:
      - data:/var/lib/haetae
    read_only: true
    tmpfs:
      - /tmp
    cap_drop:
      - ALL
    security_opt:
      - no-new-privileges:true

volumes:
  db:
  data:
COMPOSE

ENV_FILE="$HAETAE_DIR/.env"
if [ -f "$ENV_FILE" ]; then
	# Keep the password generated on first run; refresh the other settings.
	HAETAE_DB_PASSWORD=$(awk -F= '/^HAETAE_DB_PASSWORD=/{print substr($0, index($0,"=")+1); exit}' "$ENV_FILE")
	[ -n "$HAETAE_DB_PASSWORD" ] || die "haetae: corrupt .env: HAETAE_DB_PASSWORD missing"
else
	HAETAE_DB_PASSWORD=$(tr -dc A-Za-z0-9 </dev/urandom | head -c 32)
	[ "${#HAETAE_DB_PASSWORD}" -eq 32 ] || die "haetae: failed to generate database password"
fi

umask 077
cat >"$ENV_FILE" <<EOF
HAETAE_IMAGE=$HAETAE_IMAGE
HAETAE_HOST=$HAETAE_HOST
HAETAE_WEB_PORT=$HAETAE_WEB_PORT
HAETAE_GATEWAY_PORT=$HAETAE_GATEWAY_PORT
HAETAE_DB_PASSWORD=$HAETAE_DB_PASSWORD
EOF
chmod 0600 "$ENV_FILE"

if [ "$HAETAE_NO_PULL" != "1" ]; then
	compose pull
fi
compose up -d

# Wait up to 180s for the server container to become healthy.
i=0
while [ "$i" -lt 180 ]; do
	cid=$(compose ps -q server 2>/dev/null || true)
	if [ -n "$cid" ]; then
		status=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$cid" 2>/dev/null || true)
		if [ "$status" = "healthy" ]; then
			break
		fi
		if [ "$status" = "exited" ] || [ "$status" = "dead" ]; then
			compose logs server >&2 || true
			die "haetae: server container stopped unexpectedly ($status)"
		fi
	fi
	i=$((i + 1))
	sleep 1
done
if [ "$i" -ge 180 ]; then
	compose logs >&2 || true
	die "haetae: timed out waiting for the server to become healthy"
fi

URL="https://${HAETAE_HOST}:${HAETAE_WEB_PORT}"
SETUP_CODE=""
if code=$(compose exec -T server /usr/local/bin/haetae-server setup-code 2>/dev/null); then
	SETUP_CODE=$(printf '%s' "$code" | tr -d '\r\n')
fi

cat <<EOF

Haetae is ready.

  URL:        $URL
EOF
if [ -n "$SETUP_CODE" ]; then
	cat <<EOF
  Setup code: $SETUP_CODE

Open the URL in a browser and enter the setup code to create the first
administrator. The browser will warn about the certificate because it is
issued by the server's own CA (unless you supply your own; see
docs/self-hosting.md).
EOF
else
	cat <<EOF

Setup is already done. Open the URL to sign in.
The browser will warn about the certificate because it is issued by the
server's own CA (unless you supply your own; see docs/self-hosting.md).
EOF
fi

cat <<EOF

Files are in $HAETAE_DIR (compose.yaml and .env).
EOF
