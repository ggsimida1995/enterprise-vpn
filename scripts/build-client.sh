#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT_DIR=${OUT_DIR:-"$ROOT_DIR/dist"}
VERSION=${VERSION:-dev}
TARGETS=${TARGETS:-"darwin/arm64 darwin/amd64 windows/amd64 linux/amd64 linux/arm64"}

mkdir -p "$OUT_DIR"

for target in $TARGETS; do
	OS=${target%/*}
	ARCH=${target#*/}
	EXT=
	if [ "$OS" = "windows" ]; then
		EXT=.exe
	fi
	OUTPUT="$OUT_DIR/enterprise-vpn-client-$OS-$ARCH$EXT"
	echo "building $OUTPUT"
	(
		cd "$ROOT_DIR"
		CGO_ENABLED=0 GOOS="$OS" GOARCH="$ARCH" \
			go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
			-o "$OUTPUT" ./client
	)
done
