# syntax=docker/dockerfile:1
# ------------------------------------------------------------------------------
# Servy — optimized multi-stage build (~500MB vs 2GB)
#   1. frontend  : Vite build (embedded into the binary via go:embed)
#   2. build     : one static Go binary (panel + node agent) with the SPA embedded
#   3. runtime   : debian:bookworm-slim with Temurin JREs (8, 11, 17, 21)
#                  + SteamCMD (amd64) for local installs/updates
#                  No Docker CLI — the panel talks to the Docker Engine API
#                  directly over the mounted socket (mount /var/run/docker.sock)
#                  Windows-only Steam games run in sibling containers built from
#                  the self-made servy-wine image (docker/steamcmd-wine).
# ------------------------------------------------------------------------------

FROM node:22-alpine AS frontend
WORKDIR /ui
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# go.mod requires go 1.26.0 — keep this tag in sync with it and with the
# go-version pins in .github/workflows/*.yml
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /ui/dist internal/spa/dist

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
# one binary: the panel, and the node agent via `servy --agent …`
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w \
        -X github.com/FlameInTheDark/servy/internal/version.Version=${VERSION} \
        -X github.com/FlameInTheDark/servy/internal/version.Commit=${COMMIT} \
        -X github.com/FlameInTheDark/servy/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/servy ./cmd/servy

# --- Runtime: debian-slim with Temurin JREs only ---
FROM debian:bookworm-slim

# bash + pipefail: a failed curl in a `curl | tar` RUN must break the build
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

# Java versions to install (space-separated major versions)
# Minecraft: ≤1.16→8, 1.17→16, 1.18-1.20.4→17, 1.20.5+→21, latest→25
# Default: 8, 11, 17, 21 (covers 99% of servers). Override with --build-arg JAVA_VERSIONS="8 17 21"
ARG JAVA_VERSIONS="8 11 17 21"

ENV DEBIAN_FRONTEND=noninteractive
ENV LOG4J_FORMAT_MSG_NO_LOOKUPS=true
ENV HOME=/data
ENV LANG=C.UTF-8
ENV LC_ALL=C.UTF-8

# Install minimal deps
# hadolint ignore=DL3008
RUN set -eux; \
    apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl tini tzdata locales procps unzip wget gpg \
    && rm -rf /var/lib/apt/lists/* \
    && echo "en_US.UTF-8 UTF-8" >> /etc/locale.gen \
    && locale-gen en_US.UTF-8 \
    && update-locale LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8

# Install SteamCMD (amd64 only - requires 32-bit libs). Used by the panel for
# local installs/updates; containerized installs go through the official
# steamcmd/steamcmd image via the docker backend. The steamcmd manager reuses
# this copy at /opt/steamcmd instead of re-downloading into the data dir.
# hadolint ignore=DL3008
RUN if [ "${TARGETARCH}" = "amd64" ]; then \
      apt-get update && apt-get install -y --no-install-recommends lib32gcc-s1 lib32stdc++6 \
      && rm -rf /var/lib/apt/lists/* \
      && mkdir -p /opt/steamcmd \
      && curl -fsSL https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz \
         | tar -xz -C /opt/steamcmd; \
    fi

ENV PATH="/opt/steamcmd:${PATH}"

# Install Temurin JREs for each requested major version
# Using parallel downloads with retries for reliability
# hadolint ignore=DL3008,SC2086
RUN set -eux; \
    mkdir -p /opt/java; \
    case "${TARGETARCH}" in \
      amd64) TARCH=x64 ;; \
      arm64) TARCH=aarch64 ;; \
      *) echo "unsupported arch ${TARGETARCH}"; exit 1 ;; \
    esac; \
    for VER in ${JAVA_VERSIONS}; do \
      echo "Installing Temurin JRE ${VER}..."; \
      if [ "${VER}" = "25" ]; then \
        VER=21; \
      fi; \
      # Retry up to 3 times with 5s delay
      for attempt in 1 2 3; do \
        URL="https://api.adoptium.net/v3/binary/latest/${VER}/ga/linux/${TARCH}/jre/hotspot/normal/eclipse?project=jdk"; \
        if curl -fsSL --retry 2 --retry-delay 5 "${URL}" -o /tmp/jre-${VER}.tar.gz; then \
          mkdir -p /tmp/jre-${VER} \
          && tar -xzf /tmp/jre-${VER}.tar.gz --strip-components=1 -C /tmp/jre-${VER} \
          && mv /tmp/jre-${VER} "/opt/java/${VER}" \
          && rm -f /tmp/jre-${VER}.tar.gz \
          && "/opt/java/${VER}/bin/java" -version 2>&1 | head -n1 \
          && break; \
        fi; \
        echo "Attempt ${attempt} failed for Java ${VER}, retrying..."; \
        sleep 5; \
      done; \
      if [ ! -d "/opt/java/${VER}" ]; then \
        echo "WARNING: Failed to install Java ${VER} after 3 attempts"; \
      fi; \
    done; \
    echo "Installed JVMs:"; ls -la /opt/java/ 2>/dev/null || true; \
    rm -rf /tmp/jre-*

# Create version symlinks for java_selection (8, 11, 16, 17, 21, 25)
# Note: 16 maps to 17 (MC 1.17 works on Java 17 with --add-opens)
#       25 maps to 21 (no 25 JRE yet)
RUN set -eux; \
    [ -d "/opt/java/16" ] || { [ -d "/opt/java/17" ] && ln -sfn "/opt/java/17" "/opt/java/16"; }; \
    [ -d "/opt/java/25" ] || { [ -d "/opt/java/21" ] && ln -sfn "/opt/java/21" "/opt/java/25"; }

COPY --from=build /out/servy /usr/local/bin/servy

# Create user and data dir
RUN useradd -M -u 1000 -s /bin/bash servy \
    && mkdir -p /data \
    && chown -R servy:root /data

ENV PATH="/usr/local/bin:${PATH}"

WORKDIR /data
VOLUME ["/data"]

# panel 8443 · cluster node API 5055 (grpc_port) · Bedrock 19132/udp · game range
EXPOSE 8443 5055 19132/udp 25500-25600

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8443/status || exit 1

LABEL org.opencontainers.image.title="Servy" \
      org.opencontainers.image.description="Game server management panel — optimized runtime" \
      org.opencontainers.image.version=${VERSION} \
      org.opencontainers.image.revision=${COMMIT} \
      org.opencontainers.image.created=${BUILD_DATE} \
      org.opencontainers.image.source="https://github.com/FlameInTheDark/servy" \
      org.opencontainers.image.licenses="MIT"

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/servy"]
CMD ["--data", "/data", "serve"]