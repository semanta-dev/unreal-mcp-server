#!/usr/bin/env bash
# Build release archives for every target into dist/release/, plus SHA256SUMS.
#
#   scripts/package.sh [version]     # version defaults to `git describe --tags --always`
#
# Each archive holds the static server binary, README.md and CHANGELOG.md. The UnrealMCP
# editor plugin is shipped as its own source archive (it is compiled inside the game
# project with `build strategy=ubt`).
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-$(git describe --tags --always)}"
COMMIT="$(git rev-parse --short HEAD)"
PKG=github.com/jdziat/unreal-mcp-server/internal/version
OUT=dist/release
TARGETS="windows/amd64 windows/arm64 linux/amd64 darwin/arm64"

rm -rf "$OUT"
mkdir -p "$OUT"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

for t in $TARGETS; do
  os="${t%/*}"
  arch="${t#*/}"
  name="unreal-mcp_${VERSION}_${os}_${arch}"
  dir="$stage/$name"
  mkdir -p "$dir"
  exe=unreal-mcp
  [ "$os" = windows ] && exe=unreal-mcp.exe
  echo "[package] $name"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X $PKG.Version=$VERSION -X $PKG.Commit=$COMMIT" -o "$dir/$exe" ./cmd/unreal-mcp
  cp README.md CHANGELOG.md "$dir/"
  if [ "$os" = windows ]; then
    (cd "$stage" && zip -qr "$OLDPWD/$OUT/$name.zip" "$name")
  else
    tar -C "$stage" -czf "$OUT/$name.tar.gz" "$name"
  fi
done

plugin="UnrealMCP-plugin_${VERSION}"
echo "[package] $plugin"
(cd plugin && zip -qr "../$OUT/$plugin.zip" UnrealMCP -x 'UnrealMCP/Binaries/*' 'UnrealMCP/Intermediate/*')

(cd "$OUT" && sha256sum -- *.zip *.tar.gz > SHA256SUMS)
echo "[package] done:"
ls -l "$OUT"
