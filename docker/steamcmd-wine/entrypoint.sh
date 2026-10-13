#!/usr/bin/env bash
# ==============================================================================
# Servy universal SteamCMD + Wine entrypoint for Windows game servers.
#
# Works for ANY Steam game server, two modes:
#
#   1. Command mode   — args are the server command:
#        docker run servy-wine wine /server/game.exe -port 7777
#        docker run servy-wine /server/run.sh
#      Commands ending in .exe/.bat/.cmd/.msi are executed with Wine
#      automatically. The working directory is whatever you set
#      (docker run -w ...); Servy binds the server dir and sets -w /data.
#
#   2. Environment mode — no args; install via SteamCMD then run:
#        docker run -e APPID=2857200 -e RUN_CMD='./game.exe -port 7777' \
#                   -v ./gamefiles:/server servy-wine
#
# Environment variables:
#   APPID              Steam AppID to install/update (env mode, required)
#   RUN_CMD            Command to run the server, e.g. './AbioticFactor/Binaries/Win64/AbioticFactorServer-Win64-Shipping.exe -log'
#   INSTALL_DIR        Where the game lives / is installed (default /server)
#   AUTO_UPDATE        "true" → steamcmd update on every start (default false)
#   STEAM_USER         Steam username (default anonymous)
#   STEAM_PASS         Steam password (optional; anonymous if empty)
#   STEAM_GUARD        Steam Guard code (optional; MUST be paired with STEAM_USER/PASS)
#   PLATFORM           SteamCMD depot platform: windows (default) or linux
#   STEAMCMD_ARGS      Extra raw steamcmd arguments (optional)
#   INSTALL_RETRIES    steamcmd download attempts (default 3)
#   ADDITIONAL_ARGS    Extra arguments appended to RUN_CMD
#   WINE_PREFIX        Wine prefix path (default /home/gameserver/.wine)
#   WINEARCH           Wine architecture: win64 (default) / win32
#   WINEDEBUG          Wine debug channels (default -all)
#   DISPLAY            Use an existing X display, else Xvfb :99 is started
# ==============================================================================
set -uo pipefail

APPID="${APPID:-}"
RUN_CMD="${RUN_CMD:-}"
INSTALL_DIR="${INSTALL_DIR:-/server}"
AUTO_UPDATE="${AUTO_UPDATE:-false}"
STEAM_USER="${STEAM_USER:-anonymous}"
STEAM_PASS="${STEAM_PASS:-}"
STEAM_GUARD="${STEAM_GUARD:-}"
PLATFORM="${PLATFORM:-windows}"
STEAMCMD_ARGS="${STEAMCMD_ARGS:-}"
INSTALL_RETRIES="${INSTALL_RETRIES:-3}"
ADDITIONAL_ARGS="${ADDITIONAL_ARGS:-}"

export WINEPREFIX="${WINEPREFIX:-${WINE_PREFIX:-${HOME}/.wine}}"
export WINEARCH="${WINEARCH:-win64}"
export WINEDEBUG="${WINEDEBUG:--all}"
# Silence wine-mono / wine-gecko installer popups (headless-safe)
export WINEDLLOVERRIDES="${WINEDLLOVERRIDES:-mscoree,mshtml=}"

log() { printf '[servy-wine] %s\n' "$*" >&2; }
die() { log "FATAL: $*"; exit 1; }

CHILD_PID=""

# ---- Graceful shutdown -------------------------------------------------------
# Wine does not propagate SIGTERM to Windows processes, so on TERM/INT we
# nudge the child and then force wineserver to shut everything down.
# (invoked via the trap below — unreachable to shellcheck's eyes)
# shellcheck disable=SC2329,SC2317
shutdown() {
        log "shutting down (SIGTERM/SIGINT received)"
        if [[ -n "$CHILD_PID" ]] && kill -0 "$CHILD_PID" 2>/dev/null; then
                kill -TERM "$CHILD_PID" 2>/dev/null || true
        fi
        if command -v wineserver >/dev/null 2>&1; then
                wineserver -k 2>/dev/null || true
        fi
}
trap shutdown TERM INT

# ---- Wine prefix --------------------------------------------------------------
# The image bakes a prefix at $HOME/.wine owned by the build-time uid (1000).
# When the container runs as a DIFFERENT uid (servy starts sibling containers
# with the panel's own uid:gid so the bind-mounted server dir stays writable)
# that prefix is not writable — re-initialize a fresh one under /tmp (private
# to this container, recreated on start).
ensure_wine_prefix() {
        if [[ -d "$WINEPREFIX" && -w "$WINEPREFIX" ]]; then
                return
        fi
        log "wine prefix $WINEPREFIX missing or not writable for uid=$(id -u) — initializing /tmp/wine-prefix"
        export WINEPREFIX="/tmp/wine-prefix"
        export HOME="${TMPDIR:-/tmp}"
        if [[ ! -f "$WINEPREFIX/system.reg" ]]; then
                mkdir -p "$WINEPREFIX"
                if command -v xvfb-run >/dev/null 2>&1; then
                        xvfb-run -a wineboot --init
                else
                        wineboot --init
                fi
                wineserver -w
                log "fresh wine prefix ready at $WINEPREFIX"
        fi
}

# ---- Display: headless X server ----------------------------------------------
setup_display() {
        if [[ -n "${DISPLAY:-}" && "${DISPLAY}" != ":99" ]]; then
                log "using provided DISPLAY=${DISPLAY}"
                return
        fi
        if ! command -v Xvfb >/dev/null 2>&1; then
                log "Xvfb not installed — running truly headless"
                unset DISPLAY
                return
        fi
        if [[ -S /tmp/.X11-unix/X99 ]]; then
                log "reusing existing Xvfb on :99"
        else
                Xvfb :99 -screen 0 1024x768x24 -nolisten tcp >/dev/null 2>&1 &
                XVFB_PID=$!
                for _ in $(seq 1 40); do
                        [[ -S /tmp/.X11-unix/X99 ]] && break
                        sleep 0.25
                done
                log "Xvfb started on :99 (pid ${XVFB_PID:-?})"
        fi
        export DISPLAY=:99
}

# ---- SteamCMD ----------------------------------------------------------------
steamcmd_bin() {
        # Valve tarball location (this image) → wrapper → fallback to PATH
        if [[ -x /opt/steamcmd/steamcmd.sh ]]; then
                printf '%s' /opt/steamcmd/steamcmd.sh
        else
                command -v steamcmd || true
        fi
}

# ---- Steam client library wiring (steamclient.so) ------------------------------
# Native Linux game servers load the Steamworks SDK at runtime; when
# steamclient.so is not reachable the server dies right after boot with
# "GameServer.Init() failed" (valheim, rust, ark:se, ...). Wire it up the
# way Valve's own start scripts do:
#   1. put the directory holding steamclient.so on LD_LIBRARY_PATH, and
#   2. symlink it to $HOME/.steam/sdk64|sdk32/steamclient.so — some SDK
#      builds dlopen that exact path and never consult the search path.
# Preference: the game's own SDK redist in the install dir (correct arch),
# then the steamcmd bootstrap copy.
setup_steam_client() {
        local root="${1:-$PWD}"
        local lib="" kind=""
        if [[ -f "$root/linux64/steamclient.so" ]]; then
                lib="$root/linux64/steamclient.so"; kind="sdk64"
        elif [[ -f "$root/steamclient.so" ]]; then
                lib="$root/steamclient.so"; kind="sdk64"
        elif [[ -f "$root/linux32/steamclient.so" ]]; then
                lib="$root/linux32/steamclient.so"; kind="sdk32"
        elif [[ -f /opt/steamcmd/linux64/steamclient.so ]]; then
                lib="/opt/steamcmd/linux64/steamclient.so"; kind="sdk64"
        else
                log "no steamclient.so under $root or /opt/steamcmd — skipping steam client wiring"
                return 0
        fi

        local dir
        dir="$(dirname "$lib")"
        case ":${LD_LIBRARY_PATH:-}:" in
                *":$dir:"*) : ;;
                *) export LD_LIBRARY_PATH="${LD_LIBRARY_PATH:+${LD_LIBRARY_PATH}:}${dir}" ;;
        esac

        # $HOME may be unwritable when the container runs as an arbitrary uid;
        # the LD_LIBRARY_PATH entry above is enough for the search-path dlopen,
        # so the sdk-dir symlink is best-effort only.
        local sdk_dir="${HOME:-/tmp}/.steam/${kind}"
        if mkdir -p "$sdk_dir" 2>/dev/null; then
                ln -sfn "$lib" "${sdk_dir}/steamclient.so"
                log "steamclient.so wired: ${lib} (LD_LIBRARY_PATH += ${dir}, ${sdk_dir}/steamclient.so linked)"
        else
                log "cannot write ${sdk_dir} (uid $(id -u) lacks HOME write access) — relying on LD_LIBRARY_PATH=${dir}"
        fi
}

install_app() {
        local app_id="$1"
        local sc
        sc="$(steamcmd_bin)" || true
        [[ -n "$sc" ]] || die "steamcmd binary not found"

        # Order matters: platform directive first, then force_install_dir,
        # then (optional) set_steam_guard_code BEFORE login, then login.
        local -a args=()
        if [[ -n "${PLATFORM}" ]]; then
                args+=("+@sSteamCmdForcePlatformType" "${PLATFORM}")
        fi
        args+=("+force_install_dir" "${INSTALL_DIR}")
        if [[ -n "${STEAM_GUARD}" ]]; then
                args+=("+set_steam_guard_code" "${STEAM_GUARD}")
        fi
        args+=("+login" "${STEAM_USER}")
        if [[ -n "${STEAM_PASS}" ]]; then
                args+=("${STEAM_PASS}")
        fi
        args+=("+app_update" "${app_id}" "validate" "+quit")

        local attempt
        for ((attempt = 1; attempt <= INSTALL_RETRIES; attempt++)); do
                log "steamcmd install/update of app ${app_id} (attempt ${attempt}/${INSTALL_RETRIES})"
                # shellcheck disable=SC2086
                if "$sc" "${args[@]}" ${STEAMCMD_ARGS}; then
                        if [[ -d "${INSTALL_DIR}" && -n "$(ls -A "${INSTALL_DIR}" 2>/dev/null)" ]]; then
                                log "install complete"
                                return 0
                        fi
                        log "steamcmd exited 0 but ${INSTALL_DIR} is empty — treating as failure"
                else
                        log "steamcmd exited with an error"
                fi
                sleep 5
        done
        return 1
}

dir_has_content() {
        [[ -d "$1" && -n "$(ls -A "$1" 2>/dev/null)" ]]
}

# ---- Run the server ------------------------------------------------------------
is_windows_cmd() {
        case "$1" in
        *.exe | *.bat | *.cmd | *.msi) return 0 ;;
        *) return 1 ;;
        esac
}

run_windows() {
        ensure_wine_prefix
        log "executing via wine: $*"
        wine "$@"
}

# Environment mode: RUN_CMD (+ ADDITIONAL_ARGS) as a shell string.
run_env_cmd() {
        local first_word
        first_word="${RUN_CMD%% *}"
        # strip common relative-path prefixes for the suffix test
        local base="${first_word#./}"
        base="${base##*/}"
        if is_windows_cmd "$base"; then
                # shellcheck disable=SC2086
                run_windows ${RUN_CMD} ${ADDITIONAL_ARGS}
        else
                log "executing: ${RUN_CMD} ${ADDITIONAL_ARGS}"
                # shellcheck disable=SC2086
                exec ${RUN_CMD} ${ADDITIONAL_ARGS}
        fi
}

main() {
        setup_display

        # ---- Command mode ----
        if [[ $# -gt 0 ]]; then
                setup_steam_client "$PWD"
                if is_windows_cmd "$1"; then
                        ensure_wine_prefix
                        run_windows "$@" &
                else
                        "$@" &
                fi
                CHILD_PID=$!
                wait "$CHILD_PID"
                local rc=$?
                [[ -n "${XVFB_PID:-}" ]] && kill "$XVFB_PID" 2>/dev/null
                exit "$rc"
        fi

        # ---- Environment mode ----
        [[ -n "${APPID}" ]] || die "no command arguments and no APPID set — nothing to do"
        [[ -n "${RUN_CMD}" ]] || die "RUN_CMD must be specified (e.g. RUN_CMD='./server.exe -port 7777')"

        if [[ "${SKIP_INSTALL:-false}" == "true" ]]; then
                log "SKIP_INSTALL=true — skipping steamcmd"
        elif dir_has_content "${INSTALL_DIR}" && [[ "${AUTO_UPDATE}" != "true" ]]; then
                log "${INSTALL_DIR} already populated — skipping install (set AUTO_UPDATE=true to force)"
        else
                if install_app "${APPID}"; then
                        log "game ${APPID} is installed"
                elif dir_has_content "${INSTALL_DIR}"; then
                        log "install had errors but ${INSTALL_DIR} has content — attempting to start anyway"
                else
                        die "steamcmd failed to install app ${APPID} into ${INSTALL_DIR}"
                fi
        fi

        cd "${INSTALL_DIR}" || die "cannot cd into ${INSTALL_DIR}"
        setup_steam_client "$INSTALL_DIR"
        run_env_cmd &
        CHILD_PID=$!
        wait "$CHILD_PID"
        local rc=$?
        [[ -n "${XVFB_PID:-}" ]] && kill "$XVFB_PID" 2>/dev/null
        # give wine a moment to flush, then make sure nothing lingers
        if command -v wineserver >/dev/null 2>&1; then
                wineserver -k 2>/dev/null || true
        fi
        log "server exited with code ${rc}"
        exit "$rc"
}

main "$@"
