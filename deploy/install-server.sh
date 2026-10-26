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

# POSIX single-quote for .env values (escape ' as '\'').
env_quote() {
	printf "'"
	rest=$1
	while [ -n "$rest" ]; do
		case "$rest" in
		*"'"*)
			printf '%s' "${rest%%\'*}"
			printf "'\\''"
			rest=${rest#*\'}
			;;
		*)
			printf '%s' "$rest"
			rest=
			;;
		esac
	done
	printf "'"
}

# Undo env_quote / Compose single-quoted .env values (including '\'' → ').
env_unquote() {
	v=$1
	case "$v" in
	\'*\')
		v=${v#\'}
		v=${v%\'}
		;;
	esac
	out=
	while [ -n "$v" ]; do
		case "$v" in
		*"'\\''"*)
			out=$out${v%%\'\\\'\'*}
			out=$out\'
			v=${v#*\'\\\'\'}
			;;
		*)
			out=$out$v
			v=
			;;
		esac
	done
	printf '%s' "$out"
}

valid_host() {
	# Hostname labels, IPv4, or IPv6 characters (colons, hex, dots, percent zone).
	case "$1" in
	'' | *[!A-Za-z0-9._:\[\]%-]*) return 1 ;;
	esac
	return 0
}

valid_image() {
	# Docker image reference: registry/name:tag@sha256:… characters only.
	case "$1" in
	'' | *[!A-Za-z0-9._/:@-]*) return 1 ;;
	esac
	return 0
}

valid_port() {
	case "$1" in
	'' | *[!0-9]*) return 1 ;;
	esac
	[ "$1" -ge 1 ] && [ "$1" -le 65535 ]
}

valid_project() {
	case "$1" in
	'' | *[!a-z0-9_-]*) return 1 ;;
	esac
	return 0
}

need_cmd docker
docker compose version >/dev/null 2>&1 || die "haetae: 'docker compose' (Compose v2) is required"

valid_host "$HAETAE_HOST" || die "haetae: invalid HAETAE_HOST (hostname or IP characters only)"
valid_image "$HAETAE_IMAGE" || die "haetae: invalid HAETAE_IMAGE (image reference characters only)"
valid_port "$HAETAE_WEB_PORT" || die "haetae: invalid HAETAE_WEB_PORT (integer 1-65535)"
valid_port "$HAETAE_GATEWAY_PORT" || die "haetae: invalid HAETAE_GATEWAY_PORT (integer 1-65535)"
valid_project "$COMPOSE_PROJECT_NAME" || die "haetae: invalid COMPOSE_PROJECT_NAME (lowercase letters, digits, dash, underscore)"

export COMPOSE_PROJECT_NAME
# haetae.env is also read for interpolation so that HAETAE_VULN_DB_URL and
# HAETAE_GATEWAY_NAMES set there override the defaults in compose.yaml.
# (Plain env_file entries cannot override a value in "environment:".)
compose() {
	docker compose --project-directory "$HAETAE_DIR" --env-file "$HAETAE_DIR/.env" --env-file "$HAETAE_DIR/haetae.env" -f "$HAETAE_DIR/compose.yaml" "$@"
}

# Create the user settings file once; never overwrite it (it may hold tokens).
create_user_env() {
	mkdir -p "$HAETAE_DIR"
	if [ ! -e "$HAETAE_DIR/haetae.env" ]; then
		: >"$HAETAE_DIR/haetae.env"
		chmod 0600 "$HAETAE_DIR/haetae.env"
		cat >"$HAETAE_DIR/haetae.env" <<'USERENV'
# Haetae server settings. Uncomment and edit, then run: sh install-server.sh
# (or: docker compose up -d in this directory). Keep this file private: it can
# hold tokens. The installer creates it once and never overwrites it.
# One KEY=value per line, no quotes needed.

# Chat: Telegram bot token
# HAETAE_TELEGRAM_TOKEN=
# Chat: Slack bot token (xoxb-...) and app-level token (xapp-..., Socket Mode)
# HAETAE_SLACK_BOT_TOKEN=
# HAETAE_SLACK_APP_TOKEN=
# Chat: email (SMTP server host:port, sender address, login)
# HAETAE_SMTP_ADDR=smtp.example.com:587
# HAETAE_SMTP_FROM=haetae@example.com
# HAETAE_SMTP_USER=
# HAETAE_SMTP_PASS=
# HAETAE_SMTP_INSECURE=1 allows plain-text SMTP (testing only)
# Chat: Signal (URL of your signal-cli REST add-on)
# HAETAE_SIGNAL_URL=

# AI provider (without these only rule-based alerts run)
# HAETAE_AI_PROVIDER=
# HAETAE_AI_BASE_URL=
# HAETAE_AI_MODEL=
# HAETAE_AI_API_KEY=

# Web TLS: your own certificate and key (paths inside the container)
# HAETAE_WEB_TLS_CERT=
# HAETAE_WEB_TLS_KEY=
# Reverse proxies (addresses or CIDRs) whose X-Forwarded-For is trusted
# HAETAE_TRUSTED_PROXIES=

# Vulnerability database listing URL (default: Anchore; "off" disables downloads)
# HAETAE_VULN_DB_URL=
# Extra names for the gateway certificate (default: HAETAE_HOST)
# HAETAE_GATEWAY_NAMES=
USERENV
		printf 'haetae: created %s/haetae.env for your settings (chat, AI, TLS); it is kept on upgrades.\n' "$HAETAE_DIR"
	fi
	chmod 0600 "$HAETAE_DIR/haetae.env"
}

create_user_env

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
    image: postgres:17-alpine@sha256:eee6c6e1b0894fa4fe0075eb054af9fd1cf39a35321c2d4110626cfe506411fb
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
    env_file:
      - haetae.env
    environment:
      HAETAE_DATABASE_URL: postgres://haetae:${HAETAE_DB_PASSWORD:?HAETAE_DB_PASSWORD is required}@db:5432/haetae?sslmode=disable
      HAETAE_WEB_ADDR: ":8080"
      HAETAE_GATEWAY_ADDR: ":8443"
      HAETAE_WEB_URL: https://${HAETAE_HOST:?HAETAE_HOST is required}:${HAETAE_WEB_PORT:?HAETAE_WEB_PORT is required}
      HAETAE_GATEWAY_URL: https://${HAETAE_HOST:?HAETAE_HOST is required}:${HAETAE_GATEWAY_PORT:?HAETAE_GATEWAY_PORT is required}
      HAETAE_GATEWAY_NAMES: ${HAETAE_GATEWAY_NAMES:-${HAETAE_HOST:?HAETAE_HOST is required}}
      HAETAE_VULN_DB_URL: ${HAETAE_VULN_DB_URL:-https://grype.anchore.io/databases/v6/latest.json}
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
	raw=$(awk -F= '/^HAETAE_DB_PASSWORD=/{print substr($0, index($0,"=")+1); exit}' "$ENV_FILE")
	HAETAE_DB_PASSWORD=$(env_unquote "$raw")
	[ -n "$HAETAE_DB_PASSWORD" ] || die "haetae: corrupt .env: HAETAE_DB_PASSWORD missing"
else
	HAETAE_DB_PASSWORD=$(tr -dc A-Za-z0-9 </dev/urandom | head -c 32)
	[ "${#HAETAE_DB_PASSWORD}" -eq 32 ] || die "haetae: failed to generate database password"
fi

umask 077
{
	printf 'HAETAE_IMAGE=%s\n' "$(env_quote "$HAETAE_IMAGE")"
	printf 'HAETAE_HOST=%s\n' "$(env_quote "$HAETAE_HOST")"
	printf 'HAETAE_WEB_PORT=%s\n' "$(env_quote "$HAETAE_WEB_PORT")"
	printf 'HAETAE_GATEWAY_PORT=%s\n' "$(env_quote "$HAETAE_GATEWAY_PORT")"
	printf 'HAETAE_DB_PASSWORD=%s\n' "$(env_quote "$HAETAE_DB_PASSWORD")"
} >"$ENV_FILE"
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

Files are in $HAETAE_DIR (compose.yaml, .env and haetae.env for your settings).
EOF
