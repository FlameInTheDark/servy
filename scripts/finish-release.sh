#!/usr/bin/env bash
# Finish the 0.1.0 release: remaining platforms, packaging, checksums.
# (linux/amd64 binaries are already built by the caller.)
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$HOME/sdk/go/bin:$HOME/bin:$HOME/go/bin:$PATH"

VER="${1:-0.1.0}"
COMMIT="$(git rev-parse --short HEAD)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDF="-s -w -X github.com/FlameInTheDark/servy/internal/version.Version=${VER} -X github.com/FlameInTheDark/servy/internal/version.Commit=${COMMIT} -X github.com/FlameInTheDark/servy/internal/version.BuildDate=${DATE}"

build() { # goos goarch archive
  local goos="$1" goarch="$2" archive="$3"
  local name="servy_${VER}_${goos}_${goarch}"
  echo "==> ${goos}/${goarch}"
  rm -rf "dist/${name}" && mkdir -p "dist/${name}"
  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" go build -trimpath -ldflags "${LDF}" -o "dist/${name}/servy" ./cmd/servy
  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" go build -trimpath -ldflags "${LDF}" -o "dist/${name}/servy-agent" ./cmd/servy-agent
  cp README.md LICENSE CHANGELOG.md "dist/${name}/" 2>/dev/null || true
  if [ "${goos}" = "windows" ]; then
    mv "dist/${name}/servy" "dist/${name}/servy.exe"
    mv "dist/${name}/servy-agent" "dist/${name}/servy-agent.exe"
    ( cd dist && zip -qr "${name}.zip" "${name}" )
  else
    tar -czf "dist/${name}.tar.gz" -C dist "${name}"
  fi
  rm -rf "dist/${name}"
}

# linux/amd64: package the binaries the caller already produced
NAME="servy_${VER}_linux_amd64"
cp README.md LICENSE CHANGELOG.md "dist/${NAME}/" 2>/dev/null || true
tar -czf "dist/${NAME}.tar.gz" -C dist "${NAME}"
rm -rf "dist/${NAME}"
echo "==> linux/amd64 packaged"

build linux arm64 tar.gz
build darwin amd64 tar.gz
build darwin arm64 tar.gz
build windows amd64 zip

( cd dist && sha256sum servy_*.tar.gz servy_*.zip > SHA256SUMS )
echo "==> done"
ls -la dist/
