#!/bin/sh
# Installs haetae-agent from the control plane's /download/ tree, enrolls it,
# and optionally installs a systemd unit. Invoked as:
#   sh -s -- --server URL --token TOKEN --ca-pin PIN --web URL [--web-pin PIN]
set -eu

SERVER=
TOKEN=
CA_PIN=
WEB=
WEB_PIN=

usage() {
	echo "usage: sh -s -- --server URL --token TOKEN --ca-pin PIN --web URL [--web-pin PIN]" >&2
}

die() {
	echo "haetae-agent install: $*" >&2
	exit 1
}

while [ $# -gt 0 ]; do
	case "$1" in
	--server)
		[ $# -ge 2 ] || die "missing value for --server"
		SERVER=$2
		shift 2
		;;
	--token)
		[ $# -ge 2 ] || die "missing value for --token"
		TOKEN=$2
		shift 2
		;;
	--ca-pin)
		[ $# -ge 2 ] || die "missing value for --ca-pin"
		CA_PIN=$2
		shift 2
		;;
	--web)
		[ $# -ge 2 ] || die "missing value for --web"
		WEB=$2
		shift 2
		;;
	--web-pin)
		[ $# -ge 2 ] || die "missing value for --web-pin"
		WEB_PIN=$2
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		usage
		die "unknown flag: $1"
		;;
	esac
done

[ -n "$SERVER" ] || {
	usage
	die "--server is required"
}
[ -n "$TOKEN" ] || {
	usage
	die "--token is required"
}
[ -n "$CA_PIN" ] || {
	usage
	die "--ca-pin is required"
}
[ -n "$WEB" ] || {
	usage
	die "--web is required"
}

PREFIX=${HAETAE_PREFIX-}
if [ -z "$PREFIX" ] && [ "$(id -u)" -ne 0 ]; then
	die "must run as root (or set HAETAE_PREFIX for a prefixed install)"
fi

[ "$(uname -s)" = Linux ] || die "Linux is required (got $(uname -s))"
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required"
command -v mktemp >/dev/null 2>&1 || die "mktemp is required"

case "$(uname -m)" in
x86_64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) die "unsupported architecture: $(uname -m)" ;;
esac

# Strip trailing slashes so /download/ joins cleanly.
while [ "$WEB" != "${WEB%/}" ]; do
	WEB=${WEB%/}
done

AGENT_NAME=haetae-agent-linux-$ARCH
INVENTORY_NAME=haetae-inventory-linux-$ARCH
BIN_DIR=$PREFIX/usr/local/bin
STATE_DIR=$PREFIX/var/lib/haetae-agent
BIN=$BIN_DIR/haetae-agent
INVENTORY_BIN=$BIN_DIR/haetae-inventory

step=0
progress() {
	step=$((step + 1))
	echo "$step. $*"
}

download() {
	# $1 = remote path under $WEB, $2 = local file
	if [ -n "$WEB_PIN" ]; then
		# -k skips the public-CA check; --pinnedpubkey is the authentication.
		curl -fsS --max-time 120 -k --pinnedpubkey "$WEB_PIN" -o "$2" "$WEB/$1"
	else
		curl -fsS --max-time 120 -o "$2" "$WEB/$1"
	fi
}

# Single-quote a path for a systemd unit value (escape embedded ' as '\'').
unit_quote() {
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

progress "Downloading $AGENT_NAME and $INVENTORY_NAME"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

download "download/$AGENT_NAME" "$TMP/$AGENT_NAME"
download "download/$INVENTORY_NAME" "$TMP/$INVENTORY_NAME"
download download/SHA256SUMS "$TMP/SHA256SUMS"

# verify_sum NAME — sets SUM_LINE for the named file and checks it.
verify_sum() {
	_name=$1
	SUM_LINE=
	# Match the filename as the second field (GNU "hash  name" or "hash *name").
	while IFS= read -r line || [ -n "$line" ]; do
		[ -n "$line" ] || continue
		hash=${line%% *}
		if [ -z "$hash" ] || [ "$hash" = "$line" ]; then
			continue
		fi
		rest=${line#"$hash"}
		# Trim leading IFS whitespace without relying on xargs/sed.
		while [ "${rest# }" != "$rest" ] || [ "${rest#	}" != "$rest" ]; do
			rest=${rest# }
			rest=${rest#	}
		done
		file=${rest#\*}
		if [ "$file" = "$_name" ]; then
			SUM_LINE=$line
			break
		fi
	done <"$TMP/SHA256SUMS"
	[ -n "$SUM_LINE" ] || die "no SHA256SUMS entry for $_name (aborting; nothing installed)"
	# Verify in the temp dir so sha256sum finds the file by the name in the line.
	if ! (cd "$TMP" && echo "$SUM_LINE" | sha256sum -c - >/dev/null); then
		die "checksum mismatch for $_name (aborting; nothing installed)"
	fi
}

progress "Verifying checksums"
verify_sum "$AGENT_NAME"
verify_sum "$INVENTORY_NAME"

progress "Installing binaries to $BIN_DIR"
mkdir -p "$BIN_DIR" "$STATE_DIR"
cp "$TMP/$AGENT_NAME" "$BIN"
cp "$TMP/$INVENTORY_NAME" "$INVENTORY_BIN"
chmod 0755 "$BIN" "$INVENTORY_BIN"
chmod 0700 "$STATE_DIR"

if [ -f "$STATE_DIR/identity.json" ]; then
	progress "Already enrolled at $STATE_DIR; skipping enroll (safe to re-run for upgrades)"
else
	progress "Enrolling with the control plane"
	# Pass the token via the environment so it does not appear on argv / ps.
	HAETAE_ENROLL_TOKEN=$TOKEN "$BIN" enroll --server "$SERVER" --ca-pin "$CA_PIN" --state-dir "$STATE_DIR"
fi

if [ "${HAETAE_SKIP_SYSTEMD-}" = 1 ]; then
	progress "Skipping systemd (HAETAE_SKIP_SYSTEMD=1)"
	echo "Installed $BIN and state in $STATE_DIR."
	exit 0
fi

if [ -n "$PREFIX" ]; then
	die "HAETAE_PREFIX is set; prefix installs are for tests/packaging and cannot install a systemd unit"
fi

progress "Creating system user haetae-agent"
if ! getent passwd haetae-agent >/dev/null 2>&1; then
	useradd --system --no-create-home --shell /usr/sbin/nologin haetae-agent
fi
if getent group docker >/dev/null 2>&1; then
	echo "warning: adding haetae-agent to the docker group grants root-equivalent access on this host; the agent can run containers with any privileges" >&2
	usermod -aG docker haetae-agent
fi
if ! command -v docker >/dev/null 2>&1; then
	echo "warning: Docker is not installed; the agent needs the Docker Engine API socket" >&2
fi
chown -R haetae-agent:haetae-agent "$STATE_DIR"

UNIT_DIR=$PREFIX/etc/systemd/system
progress "Writing $UNIT_DIR/haetae-agent.service"
mkdir -p "$UNIT_DIR"
BIN_Q=$(unit_quote "$BIN")
STATE_Q=$(unit_quote "$STATE_DIR")
cat >"$UNIT_DIR/haetae-agent.service" <<EOF
[Unit]
Description=Haetae host agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=haetae-agent
ExecStart=$BIN_Q run --state-dir $STATE_Q
Restart=always
RestartSec=5
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=$STATE_Q
# The agent delegates its cgroup: it moves itself into an "agent" leaf
# (60M high / 100M max, set by the agent) and runs the inventory helper in
# sibling leaves capped at 256M. MemoryMax covers agent plus helper.
Delegate=memory pids cpu
MemoryMax=400M
ProtectKernelModules=yes
# CAP_NET_ADMIN and AF_NETLINK for the conntrack listener. The agent clears
# its ambient set at start so child processes do not inherit the capability.
AmbientCapabilities=CAP_NET_ADMIN
CapabilityBoundingSet=CAP_NET_ADMIN
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK
LockPersonality=yes

[Install]
WantedBy=multi-user.target
EOF

progress "Enabling and starting haetae-agent"
systemctl daemon-reload
systemctl enable haetae-agent
systemctl restart haetae-agent

i=0
while [ "$i" -lt 30 ]; do
	if systemctl is-active --quiet haetae-agent; then
		progress "haetae-agent is active"
		echo "Install complete."
		exit 0
	fi
	i=$((i + 1))
	sleep 1
done

die "haetae-agent failed to become active; see: journalctl -u haetae-agent"
