#!/usr/bin/env bash
# Build release archives for every supported platform into dist/.
# Usage: scripts/build-release.sh [version]   (default: from git describe)
set -euo pipefail

VERSION="${1:-$(git describe --tags --always 2>/dev/null || echo dev)}"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-s -w -X github.com/FlameInTheDark/servy/internal/version.Version=${VERSION#v} \
 -X github.com/FlameInTheDark/servy/internal/version.Commit=${COMMIT} \
 -X github.com/FlameInTheDark/servy/internal/version.BuildDate=${DATE}"

echo "==> building frontend (embedded via go:embed)"
( cd frontend && npm ci --no-audit --no-fund && npm run build )
rm -rf internal/spa/dist && cp -r frontend/dist internal/spa/dist

mkdir -p dist
build() { # goos goarch archive
  local goos="$1" goarch="$2" archive="$3"
  echo "==> servy ${VERSION} ${goos}/${goarch}"
  local name="servy_${VERSION#v}_${goos}_${goarch}"
  rm -rf "dist/${name}" && mkdir -p "dist/${name}"
  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
    go build -trimpath -ldflags "${LDFLAGS}" -o "dist/${name}/servy" ./cmd/servy
  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
    go build -trimpath -ldflags "${LDFLAGS}" -o "dist/${name}/servy-agent" ./cmd/servy-agent
  if [ "${goos}" = "windows" ]; then
    mv "dist/${name}/servy" "dist/${name}/servy.exe"
    mv "dist/${name}/servy-agent" "dist/${name}/servy-agent.exe"
    ( cd dist && zip -qr "${name}.zip" "${name}" )
  else
    tar -czf "dist/${name}.tar.gz" -C dist "${name}"
  fi
}

build linux amd64 tar.gz
build linux arm64 tar.gz
build darwin amd64 tar.gz
build darwin arm64 tar.gz
build windows amd64 zip

rm -rf dist/servy_*/ # keep only archives + checksums
( cd dist && sha256sum servy_*.tar.gz servy_*.zip > SHA256SUMS )
echo "==> done:" && ls dist/
