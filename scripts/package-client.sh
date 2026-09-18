#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VERSION=${VERSION:-dev}
OUT_DIR=${OUT_DIR:-"$ROOT_DIR/dist"}
CORE_DIR=${EASYTIER_CORE_DIR:-}

"$ROOT_DIR/scripts/build-client.sh"

for binary in "$OUT_DIR"/enterprise-vpn-client-*; do
	[ -f "$binary" ] || continue
	name=$(basename "$binary")
	case "$name" in
		*.tar.gz) continue ;;
	esac
	target=${name#enterprise-vpn-client-}
	target=${target%.exe}
	package_dir="$OUT_DIR/package-$target"
	rm -rf "$package_dir"
	mkdir -p "$package_dir"
	client_dir="$package_dir"
	launch_name=enterprise-vpn-client
	case "$target" in
		darwin-*)
			client_dir="$package_dir/Enterprise VPN.app/Contents/MacOS"
			mkdir -p "$client_dir"
			client_name=enterprise-vpn-client
			launch_name="Enterprise VPN.app"
			cat > "$package_dir/Enterprise VPN.app/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDisplayName</key><string>企业内网</string>
	<key>CFBundleExecutable</key><string>enterprise-vpn-client</string>
	<key>CFBundleIdentifier</key><string>com.enterprise.vpn.client</string>
	<key>CFBundleName</key><string>企业内网</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>$VERSION</string>
	<key>CFBundleVersion</key><string>$VERSION</string>
</dict>
</plist>
EOF
			;;
		windows-amd64)
			client_name=enterprise-vpn-client.exe
			;;
		*)
			client_name=enterprise-vpn-client
			;;
	esac
	cp "$binary" "$client_dir/$client_name"

	if [ -n "$CORE_DIR" ]; then
		core="$CORE_DIR/easytier-core-$target"
		if [ ! -f "$core" ] && [ -f "$CORE_DIR/$target/easytier-core" ]; then
			core="$CORE_DIR/$target/easytier-core"
		fi
		if [ -f "$core" ]; then
			case "$target" in
				windows-amd64) core_name=easytier-core.exe ;;
				*) core_name=easytier-core ;;
			esac
			cp "$core" "$client_dir/$core_name"
			chmod +x "$client_dir/$core_name"
		fi
	fi

	case "$target" in
		windows-amd64) launch_name=enterprise-vpn-client.exe ;;
		darwin-*) launch_name="Enterprise VPN.app" ;;
		*) launch_name=enterprise-vpn-client ;;
	esac
	cat > "$package_dir/README.txt" <<EOF
Enterprise VPN Client $VERSION

Run $launch_name and enter your account and password.
The bundled easytier-core binary is managed automatically when present.
EOF
	archive="$OUT_DIR/enterprise-vpn-client-$target.tar.gz"
	tar -C "$OUT_DIR" -czf "$archive" "package-$target"
	rm -rf "$package_dir"
done
