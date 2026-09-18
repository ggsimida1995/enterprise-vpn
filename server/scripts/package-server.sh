#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT_DIR=${OUT_DIR:-"$ROOT_DIR/dist"}
VERSION=${VERSION:-dev}
TARGETS=${TARGETS:-"linux/amd64 linux/arm64"}

mkdir -p "$OUT_DIR"
for target in $TARGETS; do
	OS=${target%/*}
	ARCH=${target#*/}
	[ "$OS" = "linux" ] || { echo "unsupported server package target: $target" >&2; exit 1; }
	package_dir="$OUT_DIR/enterprise-vpn-server-$OS-$ARCH"
	rm -rf "$package_dir"
	mkdir -p "$package_dir"
	echo "building $package_dir/enterprise-vpn-server"
	(
		cd "$ROOT_DIR"
		CGO_ENABLED=0 GOOS="$OS" GOARCH="$ARCH" \
			go build -trimpath -ldflags "-s -w" -o "$package_dir/enterprise-vpn-server" .
	)
	cp "$ROOT_DIR/server.example.json" "$package_dir/server.example.json"
	cp "$ROOT_DIR/scripts/run-server.sh" "$package_dir/run-server.sh"
	chmod +x "$package_dir/enterprise-vpn-server" "$package_dir/run-server.sh"
	cat > "$package_dir/README.txt" <<EOF
Enterprise VPN Server $VERSION

1. Copy server.example.json to server.json.
2. Run ./run-server.sh.
3. Open /admin and sign in with admin/admin, then change the initial password.

Place this service behind an HTTPS reverse proxy before client distribution.
EOF
	tar -C "$OUT_DIR" -czf "$OUT_DIR/enterprise-vpn-server-$OS-$ARCH-$VERSION.tar.gz" "enterprise-vpn-server-$OS-$ARCH"
	rm -rf "$package_dir"
done
