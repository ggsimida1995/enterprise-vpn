#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VERSION=${VERSION:-dev}
OUT_DIR=${OUT_DIR:-"$ROOT_DIR/dist"}
CORE_DIR=${EASYTIER_CORE_DIR:?set EASYTIER_CORE_DIR to the downloaded EasyTier Core directory}

"$ROOT_DIR/scripts/build-client.sh"

for binary in "$OUT_DIR"/enterprise-vpn-client-*; do
	[ -f "$binary" ] || continue
	name=$(basename "$binary")
	case "$name" in
		*.tar.gz) continue ;;
	esac
	target=${name#enterprise-vpn-client-}
	target=${target%.exe}
	core="$CORE_DIR/easytier-core-$target"
	[ -f "$core" ] || { echo "missing EasyTier Core for $target: $core" >&2; exit 1; }
	package_dir="$OUT_DIR/package-$target"
	rm -rf "$package_dir"
	mkdir -p "$package_dir"
	client_dir="$package_dir"
	case "$target" in
		darwin-*)
			client_dir="$package_dir/Enterprise VPN.app/Contents/MacOS"
			mkdir -p "$client_dir"
			client_name=enterprise-vpn-client
			cat > "$package_dir/Enterprise VPN.app/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleDisplayName</key><string>企业内网</string>
<key>CFBundleExecutable</key><string>enterprise-vpn-client</string>
<key>CFBundleIdentifier</key><string>com.enterprise.vpn.client</string>
<key>CFBundleName</key><string>企业内网</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>$VERSION</string>
<key>CFBundleVersion</key><string>$VERSION</string>
</dict></plist>
EOF
			;;
		windows-amd64)
			client_name=enterprise-vpn-client.exe
			;;
		*)
			echo "unsupported client package target: $target" >&2
			exit 1
			;;
	esac
	cp "$binary" "$client_dir/$client_name"
	case "$target" in
		windows-amd64) core_name=easytier-core.exe ;;
		*) core_name=easytier-core ;;
	esac
	cp "$core" "$client_dir/$core_name"
	chmod +x "$client_dir/$core_name"
	archive="$OUT_DIR/enterprise-vpn-client-$target.tar.gz"
	tar -C "$OUT_DIR" -czf "$archive" "package-$target"
	rm -rf "$package_dir"
done
