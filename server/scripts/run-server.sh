#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
: "${VPN_ADMIN_USER:?set VPN_ADMIN_USER}"
: "${VPN_ADMIN_PASSWORD:?set VPN_ADMIN_PASSWORD}"

exec "$ROOT_DIR/enterprise-vpn-server" \
	-addr "${LISTEN_ADDR:-:8080}" \
	-config "${STATE_FILE:-$ROOT_DIR/server.json}"
