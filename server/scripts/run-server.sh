#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

exec "$ROOT_DIR/enterprise-vpn-server" \
	-addr "${LISTEN_ADDR:-:8080}" \
	-config "${STATE_FILE:-$ROOT_DIR/server.json}"
