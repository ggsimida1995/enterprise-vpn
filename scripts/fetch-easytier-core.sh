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
    windows/amd64) upstream=windows-x86_64; output=easytier-core-windows-amd64; cli_output=easytier-cli-windows-amd64 ;;
    windows/arm64) upstream=windows-arm64; output=easytier-core-windows-arm64; cli_output=easytier-cli-windows-arm64 ;;
  esac
  archive="$TMP_DIR/$upstream.zip"
  url="https://github.com/EasyTier/EasyTier/releases/download/$EASYTIER_VERSION/easytier-$upstream-$EASYTIER_VERSION.zip"
  echo "downloading $url"
  curl --fail --location --silent --show-error "$url" -o "$archive"
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
  chmod +x "$OUT_DIR/$output"
  chmod +x "$OUT_DIR/$cli_output"
done
