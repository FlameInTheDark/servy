# syntax=docker/dockerfile:1
# ==============================================================================
# Servy Wine + SteamCMD base image  (self-made, universal for ANY game)
#
# This is the image the Pleut/abiotic-factor-linux-docker example inspired, but
# generic and actually working:
#   * ubuntu:24.04 + WineHQ wine (amd64 + i386) — the distro "wine64"-only
#     setup of the example breaks 32-bit game servers and the cm2network/steamcmd
#     base used earlier ships NO Wine at all.
#   * SteamCMD from the official Valve tarball (not the ancient distro package).
#   * Xvfb for headless operation (started automatically by the entrypoint).
#   * winetricks + cabextract for per-game prerequisites.
#   * tini as PID 1 — correct signal handling and zombie reaping for Wine.
#   * Non-root user `gameserver`, Wine prefix pre-initialized at build time so
#     first boot is fast and deterministic.
#
# Usage (standalone):
#   docker build -t servy-wine:latest .
#   docker run -d -e APPID=2857200 \
#     -e RUN_CMD='./AbioticFactor/Binaries/Win64/AbioticFactorServer-Win64-Shipping.exe -log' \
#     -v ./gamefiles:/server -p 7777:7777/udp servy-wine:latest
#
# Usage (Servy panel): referenced by the game catalog (docker_image) for
# windows-only titles; the panel binds the server dir and runs the game's
# command via `wine` automatically (see entrypoint.sh).
#
# NOTE: amd64 only — Windows game servers via Wine require amd64 + i386.
# ==============================================================================
FROM ubuntu:24.04

ARG TARGETARCH
ARG WINE_BRANCH=stable
ARG DEBIAN_FRONTEND=noninteractive

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# ---- Base tooling + Wine (WineHQ, both architectures) + Xvfb -----------------
# hadolint ignore=DL3008,SC2174
RUN if [ "${TARGETARCH}" != "amd64" ] && [ -n "${TARGETARCH}" ]; then \
      echo "ERROR: the servy-wine image only supports linux/amd64 (got ${TARGETARCH})" >&2; \
      exit 1; \
    fi \
 && dpkg --add-architecture i386 \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl gnupg xz-utils \
 && mkdir -m 755 -p /etc/apt/keyrings \
 && curl -fsSL https://dl.winehq.org/wine-builds/winehq.key \
      -o /etc/apt/keyrings/winehq-archive.key \
 && curl -fsSL https://dl.winehq.org/wine-builds/ubuntu/dists/noble/winehq-noble.sources \
      -o /etc/apt/sources.list.d/winehq.sources \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
      "winehq-${WINE_BRANCH}" \
      xvfb xauth \
      winetricks cabextract \
      tini procps locales \
 && apt-get clean autoclean \
 && apt-get autoremove -y \
 && rm -rf /var/lib/apt/lists/* \
 && locale-gen en_US.UTF-8 \
 && update-locale LANG=en_US.UTF-8

# ---- SteamCMD (official Valve tarball) ----------------------------------------
# Requires 32-bit runtime libs; i386 was enabled above.
# (the printf writes literal ${VARS} into the wrapper script on purpose)
# hadolint ignore=DL3008,SC2016
RUN apt-get update \
 && apt-get install -y --no-install-recommends lib32gcc-s1 lib32stdc++6 \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /opt/steamcmd \
 && curl -fsSL https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz \
      | tar -xz -C /opt/steamcmd \
 && printf '#!/usr/bin/env bash\n# Wrapper: force SteamCMD depot platform via env when set.\nset -euo pipefail\nARGS=()\nif [[ -n "${SteamCmdForcePlatformType:-}" ]]; then\n  ARGS+=("+@sSteamCmdForcePlatformType" "${SteamCmdForcePlatformType}")\nfi\nexec /opt/steamcmd/steamcmd.sh "${ARGS[@]}" "$@"\n' \
      > /usr/local/bin/steamcmd \
 && chmod +x /usr/local/bin/steamcmd

# ---- Non-root user --------------------------------------------------------
# Fresh user (no usermod renaming tricks): uid/gid 1000 maps to the typical
# first Linux user, keeping bind-mount permissions sane.
# ubuntu:24.04+ ships a pre-installed `ubuntu` user that already occupies
# uid/gid 1000 — remove whoever holds the target ids first, otherwise useradd
# fails with "UID 1000 is not unique" (build-breaking on noble). Override both
# ids via --build-arg if your host user differs.
# SteamCMD self-updates by writing into its own directory — /opt/steamcmd must
# be writable by the runtime user or installs break on the next Valve update.
ARG GAMESERVER_UID=1000
ARG GAMESERVER_GID=1000
RUN set -eu; \
    uid="${GAMESERVER_UID}"; gid="${GAMESERVER_GID}"; \
    if occ="$(getent passwd "${uid}")"; then \
      echo "removing pre-existing user ${occ%%:*} (uid ${uid})"; \
      userdel -r "${occ%%:*}" 2>/dev/null || true; \
    fi; \
    if grp="$(getent group "${gid}")"; then \
      echo "removing pre-existing group ${grp%%:*} (gid ${gid})"; \
      groupdel "${grp%%:*}" 2>/dev/null || true; \
    fi; \
    groupadd -g "${gid}" gameserver; \
    useradd -M -l -u "${uid}" -g "${gid}" -s /bin/bash gameserver; \
    mkdir -p /tmp/.X11-unix; chmod 1777 /tmp/.X11-unix; \
    mkdir -p /home/gameserver /server \
             /home/gameserver/.steam/sdk64 /home/gameserver/.steam/sdk32; \
    chown -R gameserver:gameserver /home/gameserver /server; \
    if [ -d /opt/steamcmd ]; then chown -R gameserver:gameserver /opt/steamcmd; fi

# ---- Environment ---------------------------------------------------------------
ENV HOME=/home/gameserver \
    LANG=en_US.UTF-8 \
    LC_ALL=en_US.UTF-8 \
    WINEPREFIX=/home/gameserver/.wine \
    WINEARCH=win64 \
    WINEDEBUG=-all \
    WINEDLLOVERRIDES="mscoree,mshtml=" \
    DISPLAY=:99 \
    PATH="/opt/steamcmd:${PATH}"

USER gameserver
WORKDIR /server

# ---- Pre-initialize the Wine prefix (no mono/gecko download prompts) ----------
RUN xvfb-run -a wineboot --init \
 && wineserver -w \
 && wine reg add "HKEY_CURRENT_USER\\Software\\Wine\\DllOverrides" /v mscoree /t REG_SZ /d "" /f \
 && wine reg add "HKEY_CURRENT_USER\\Software\\Wine\\DllOverrides" /v mshtml  /t REG_SZ /d "" /f \
 && wineserver -w \
 && rm -rf /tmp/wine-* /tmp/wine.* /tmp/.X99-lock /tmp/.X11-unix/X99

VOLUME ["/server"]

COPY --chown=gameserver:gameserver entrypoint.sh /entrypoint.sh
# Normalize shebang/line endings for build contexts checked out on Windows.
RUN tmp="$(mktemp)" \
 && sed 's/\r$//' /entrypoint.sh > "$tmp" \
 && chmod u+w /entrypoint.sh \
 && cat "$tmp" > /entrypoint.sh \
 && rm -f "$tmp" \
 && test "$(head -n 1 /entrypoint.sh)" = '#!/usr/bin/env bash' \
 && chmod +x /entrypoint.sh

# tini as PID 1: forwards signals to the entrypoint, reaps wine zombies.
ENTRYPOINT ["/usr/bin/tini", "--", "/entrypoint.sh"]
CMD ["bash"]
