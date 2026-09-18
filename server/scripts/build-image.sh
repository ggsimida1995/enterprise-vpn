#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
IMAGE=${IMAGE:-enterprise-vpn-server:latest}

docker build -t "$IMAGE" "$ROOT_DIR"
