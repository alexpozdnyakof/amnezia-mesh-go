#!/usr/bin/env bash
# Stage the files the macOS cask archive adds on top of the darwin CLI:
#   build/tui/darwin_<arch>/awg-mesh-tui
#   build/payload/linux_{amd64,arm64}/awg-mesh
# Run from anywhere. Goreleaser reads these paths (it wipes dist/, not build/).
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

tui=${AWG_MESH_TUI:-"$root/../amnezia-mesh-tui"}
archs=${AWG_MESH_DARWIN_ARCHS:-"arm64 amd64"}

if [[ ! -f "$tui/Cargo.toml" ]]; then
  echo "TUI checkout not found at $tui (set AWG_MESH_TUI)" >&2
  exit 1
fi
if ! command -v cargo >/dev/null 2>&1; then
  echo "cargo is required to build awg-mesh-tui" >&2
  exit 1
fi

version=${AWG_MESH_VERSION:-}
if [[ -z "$version" ]] && ver=$(git describe --tags --exact-match 2>/dev/null); then
  version=${ver#v}
fi
ldflags=(-s -w)
if [[ -n "$version" ]]; then
  ldflags+=("-X" "github.com/alexpozdnyakof/amnezia-mesh-go/internal/cli.Version=${version}")
fi

host_arch=$(go env GOARCH)
for arch in amd64 arm64; do
  out="$root/build/payload/linux_$arch"
  mkdir -p "$out"
  echo "payload linux/$arch"
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -trimpath -ldflags "${ldflags[*]}" -o "$out/awg-mesh" ./cmd/awg-mesh
done

rust_target() {
  case "$1" in
    arm64) echo aarch64-apple-darwin ;;
    amd64) echo x86_64-apple-darwin ;;
    *) echo "unknown darwin arch: $1" >&2; exit 1 ;;
  esac
}

for arch in $archs; do
  triple=$(rust_target "$arch")
  dest="$root/build/tui/darwin_$arch"
  mkdir -p "$dest"
  echo "tui darwin/$arch ($triple)"
  if [[ "$arch" == "$host_arch" ]]; then
    cargo build --release --manifest-path "$tui/Cargo.toml"
    cp -f "$tui/target/release/awg-mesh-tui" "$dest/awg-mesh-tui"
  else
    if command -v rustup >/dev/null 2>&1; then
      rustup target add "$triple"
    fi
    cargo build --release --target "$triple" --manifest-path "$tui/Cargo.toml"
    cp -f "$tui/target/$triple/release/awg-mesh-tui" "$dest/awg-mesh-tui"
  fi
  chmod 755 "$dest/awg-mesh-tui"
done
