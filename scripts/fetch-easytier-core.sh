#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT_DIR=${OUT_DIR:-"$ROOT_DIR/cores"}
EASYTIER_VERSION=${EASYTIER_VERSION:-v2.6.4}
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

mkdir -p "$OUT_DIR"
for target in darwin/arm64 windows/amd64 windows/arm64; do
  case "$target" in
    darwin/arm64) upstream=macos-aarch64; output=easytier-core-darwin-arm64; cli_output=easytier-cli-darwin-arm64 ;;
    windows/amd64) upstream=windows-x86_64; output=easytier-core-windows-amd64; cli_output=easytier-cli-windows-amd64; packet_output=Packet-windows-amd64.dll; wintun_output=wintun-windows-amd64.dll; windivert_output=WinDivert64-windows-amd64.sys ;;
    windows/arm64) upstream=windows-arm64; output=easytier-core-windows-arm64; cli_output=easytier-cli-windows-arm64; packet_output=Packet-windows-arm64.dll; wintun_output=wintun-windows-arm64.dll; windivert_output=WinDivert64-windows-arm64.sys ;;
  esac
  archive="$TMP_DIR/$upstream.zip"
  url="https://github.com/EasyTier/EasyTier/releases/download/$EASYTIER_VERSION/easytier-$upstream-$EASYTIER_VERSION.zip"
  echo "downloading $url"
  curl --fail --location --silent --show-error --retry 5 --retry-delay 2 --retry-all-errors "$url" -o "$archive"
  mkdir "$TMP_DIR/$upstream"
  unzip -q "$archive" -d "$TMP_DIR/$upstream"
  if [ "$target" != "darwin/arm64" ]; then
    core=$(find "$TMP_DIR/$upstream" -type f -name easytier-core.exe -print -quit)
    cli=$(find "$TMP_DIR/$upstream" -type f -name easytier-cli.exe -print -quit)
  else
    core=$(find "$TMP_DIR/$upstream" -type f -name easytier-core -print -quit)
    cli=$(find "$TMP_DIR/$upstream" -type f -name easytier-cli -print -quit)
  fi
  [ -n "$core" ] || { echo "easytier-core not found in $archive" >&2; exit 1; }
  [ -n "$cli" ] || { echo "easytier-cli not found in $archive" >&2; exit 1; }
  cp "$core" "$OUT_DIR/$output"
  cp "$cli" "$OUT_DIR/$cli_output"
  if [ "$target" != "darwin/arm64" ]; then
    packet=$(find "$TMP_DIR/$upstream" -type f -iname Packet.dll -print -quit)
    wintun=$(find "$TMP_DIR/$upstream" -type f -iname wintun.dll -print -quit)
    windivert=$(find "$TMP_DIR/$upstream" -type f -iname WinDivert64.sys -print -quit)
    [ -n "$packet" ] || { echo "Packet.dll not found in $archive" >&2; exit 1; }
    [ -n "$wintun" ] || { echo "wintun.dll not found in $archive" >&2; exit 1; }
    [ -n "$windivert" ] || { echo "WinDivert64.sys not found in $archive" >&2; exit 1; }
    cp "$packet" "$OUT_DIR/$packet_output"
    cp "$wintun" "$OUT_DIR/$wintun_output"
    cp "$windivert" "$OUT_DIR/$windivert_output"
  fi
  chmod +x "$OUT_DIR/$output"
  chmod +x "$OUT_DIR/$cli_output"
done
