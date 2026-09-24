#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
OUT_DIR=${OUT_DIR:-"$ROOT_DIR/dist"}
CORE_DIR=${EASYTIER_CORE_DIR:-"$ROOT_DIR/cores"}
RESOURCE_DIR="$ROOT_DIR/src-tauri/resources"
TARGETS=${TARGETS:-native}
BUNDLES=${BUNDLES:-}
ARTIFACT_SUFFIX=${ARTIFACT_SUFFIX:-}
mkdir -p "$OUT_DIR" "$ROOT_DIR/src-tauri/binaries"

copy_core() {
  target=$1
  windows=no
  case "$target" in
    native)
      case "$(uname -s):$(uname -m)" in
        Darwin:arm64) source="$CORE_DIR/easytier-core-darwin-arm64"; cli_source="$CORE_DIR/easytier-cli-darwin-arm64" ;;
        Darwin:x86_64) source="$CORE_DIR/easytier-core-darwin-amd64"; cli_source="$CORE_DIR/easytier-cli-darwin-amd64" ;;
        MINGW*:x86_64|MSYS*:x86_64) source="$CORE_DIR/easytier-core-windows-amd64"; cli_source="$CORE_DIR/easytier-cli-windows-amd64"; windows=yes ;;
        *) source="$CORE_DIR/easytier-core" ;;
      esac
      ;;
    darwin/arm64) source="$CORE_DIR/easytier-core-darwin-arm64"; cli_source="$CORE_DIR/easytier-cli-darwin-arm64" ;;
    windows/amd64) source="$CORE_DIR/easytier-core-windows-amd64"; cli_source="$CORE_DIR/easytier-cli-windows-amd64"; windows=yes ;;
    windows/arm64) source="$CORE_DIR/easytier-core-windows-arm64"; cli_source="$CORE_DIR/easytier-cli-windows-arm64"; windows=yes ;;
    *) echo "unsupported target: $target" >&2; exit 1 ;;
  esac

  [ -f "$source" ] || {
    echo "missing EasyTier Core: $source" >&2
    echo "run EASYTIER_VERSION=... ./scripts/fetch-easytier-core.sh first" >&2
    exit 1
  }
  [ -f "$cli_source" ] || {
    echo "missing EasyTier CLI: $cli_source" >&2
    echo "run EASYTIER_VERSION=... ./scripts/fetch-easytier-core.sh first" >&2
    exit 1
  }
  if [ "$windows" = yes ]; then
    case "$target" in
      native)
        case "$(uname -m)" in
          x86_64) runtime_suffix=windows-amd64; rust_target=x86_64-pc-windows-msvc ;;
          aarch64|arm64) runtime_suffix=windows-arm64; rust_target=aarch64-pc-windows-msvc ;;
          *) echo "unsupported native Windows architecture: $(uname -m)" >&2; exit 1 ;;
        esac
        ;;
      windows/amd64) runtime_suffix=windows-amd64; rust_target=x86_64-pc-windows-msvc ;;
      windows/arm64) runtime_suffix=windows-arm64; rust_target=aarch64-pc-windows-msvc ;;
    esac
    mkdir -p "$RESOURCE_DIR/binaries"
    find "$RESOURCE_DIR/binaries" -type f ! -name .gitkeep -delete 2>/dev/null || true
    rm -f "$RESOURCE_DIR/binaries/.gitkeep"
    for file in \
      "$CORE_DIR/Packet-${runtime_suffix}.dll" \
      "$CORE_DIR/wintun-${runtime_suffix}.dll" \
      "$CORE_DIR/WinDivert64-${runtime_suffix}.sys"
    do
      [ -f "$file" ] || { echo "missing EasyTier runtime dependency: $file" >&2; exit 1; }
    done
    cp "$source" "$ROOT_DIR/src-tauri/binaries/easytier-core-${rust_target}.exe"
    cp "$cli_source" "$ROOT_DIR/src-tauri/binaries/easytier-cli-${rust_target}.exe"
    cp "$CORE_DIR/Packet-${runtime_suffix}.dll" "$RESOURCE_DIR/binaries/Packet.dll"
    cp "$CORE_DIR/wintun-${runtime_suffix}.dll" "$RESOURCE_DIR/binaries/wintun.dll"
    cp "$CORE_DIR/WinDivert64-${runtime_suffix}.sys" "$RESOURCE_DIR/binaries/WinDivert64.sys"
  else
    mkdir -p "$RESOURCE_DIR/binaries"
    find "$RESOURCE_DIR/binaries" -type f ! -name .gitkeep -delete 2>/dev/null || true
    case "$target" in
      native)
        case "$(uname -m)" in
          arm64|aarch64) rust_target=aarch64-apple-darwin ;;
          x86_64) rust_target=x86_64-apple-darwin ;;
          *) echo "unsupported native macOS architecture: $(uname -m)" >&2; exit 1 ;;
        esac
        ;;
      darwin/arm64) rust_target=aarch64-apple-darwin ;;
      *) echo "unsupported macOS target: $target" >&2; exit 1 ;;
    esac
    cp "$source" "$ROOT_DIR/src-tauri/binaries/easytier-core-${rust_target}"
    cp "$cli_source" "$ROOT_DIR/src-tauri/binaries/easytier-cli-${rust_target}"
  fi
  chmod +x "$ROOT_DIR/src-tauri/binaries"/easytier-core* "$ROOT_DIR/src-tauri/binaries"/easytier-cli* 2>/dev/null || true
}

build_one() {
  target=$1
  copy_core "$target"
  cargo_args=
  case "$target" in
    native) bundle_dir="$ROOT_DIR/src-tauri/target/release/bundle" ;;
    darwin/arm64) cargo_args="--target aarch64-apple-darwin"; bundle_dir="$ROOT_DIR/src-tauri/target/aarch64-apple-darwin/release/bundle" ;;
    windows/amd64) cargo_args="--target x86_64-pc-windows-msvc"; bundle_dir="$ROOT_DIR/src-tauri/target/x86_64-pc-windows-msvc/release/bundle" ;;
    windows/arm64) cargo_args="--target aarch64-pc-windows-msvc"; bundle_dir="$ROOT_DIR/src-tauri/target/aarch64-pc-windows-msvc/release/bundle" ;;
  esac
  if [ -n "$BUNDLES" ]; then
    if [ "$windows" = yes ]; then
      (cd "$ROOT_DIR/src-tauri" && cargo tauri build $cargo_args --bundles "$BUNDLES")
    else
      (cd "$ROOT_DIR/src-tauri" && cargo tauri build $cargo_args --bundles "$BUNDLES")
    fi
  else
    if [ "$windows" = yes ]; then
      (cd "$ROOT_DIR/src-tauri" && cargo tauri build $cargo_args)
    else
      (cd "$ROOT_DIR/src-tauri" && cargo tauri build $cargo_args)
    fi
  fi
  if [ "$BUNDLES" = "msi" ]; then
    find "$bundle_dir" -type f -name '*.msi' -exec sh -c '
      for file do
        cp "$file" "$1/$(basename "${file%.msi}")-$2.msi"
      done
    ' sh "$OUT_DIR" "$ARTIFACT_SUFFIX" {} +
  elif [ "$BUNDLES" = "dmg" ]; then
    find "$bundle_dir" -type f -name '*.dmg' -exec sh -c '
      for file do
        cp "$file" "$1/$(basename "${file%.dmg}")-$2.dmg"
      done
    ' sh "$OUT_DIR" "$ARTIFACT_SUFFIX" {} +
  else
    find "$bundle_dir" -type f \( -name '*.dmg' -o -name '*.app.tar.gz' -o -name '*.msi' -o -name '*.exe' -o -name '*.nsis.zip' \) -exec cp {} "$OUT_DIR/" \;
  fi
}

for target in $TARGETS; do
  build_one "$target"
done
