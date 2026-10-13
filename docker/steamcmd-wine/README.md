# servy-wine — universal Windows game server base image

`SteamCMD + Wine` Docker image for running Windows-only Steam game servers on Linux.

Two variants:

| File | Base | Wine | Use for |
|------|------|------|---------|
| `Dockerfile` | ubuntu:24.04 | WineHQ `${WINE_BRANCH}` (amd64+i386) | **recommended** — modern UE5 titles (Abiotic Factor, V Rising, Enshrouded, etc.) |
| `Dockerfile.minimal` | ubuntu:24.04 | distro wine 9.0 + distro steamcmd | smaller image, older/less demanding servers |

Both share the same universal `entrypoint.sh`, run as non-root user `gameserver`, ship a pre-initialized Wine prefix (no mono/gecko popups), start `Xvfb` automatically and use `tini` for correct signal handling.

## Build

```bash
make docker-wine                                   # → servy-wine:dev
# or
docker build -t servy-wine:latest docker/steamcmd-wine
```

> **Tag it `servy-wine`, not anything else.** The bundled game catalog (`internal/steamcmd/games/*.yaml`) references `ghcr.io/flameinthedark/servy-wine:latest`. Either retag your local build (`docker tag servy-wine:latest ghcr.io/flameinthedark/servy-wine:latest`) or override the image per server in the panel UI — an image built under a different tag will not be picked up by default.

### Non-root uid/gid

The container runs as user `gameserver` (uid/gid 1000 by default — the typical first host user, so bind mounts keep sane ownership). `ubuntu:24.04+` ships a pre-installed `ubuntu` user that already occupies uid/gid 1000; the build removes that account automatically before creating `gameserver`.

If your host user has a different uid, rebuild with matching ids so bind mounted files stay writable:

```bash
docker build \
  --build-arg GAMESERVER_UID="$(id -u)" \
  --build-arg GAMESERVER_GID="$(id -g)" \
  -t servy-wine:latest docker/steamcmd-wine
```

CI publishes it to `ghcr.io/flameinthedark/servy-wine:latest` on every release.

## Standalone usage (env mode)

```bash
docker run -d --name my-server \
  -e APPID=2857200 \
  -e RUN_CMD='./AbioticFactor/Binaries/Win64/AbioticFactorServer-Win64-Shipping.exe -log -useperfthreads -PORT=7777' \
  -e ADDITIONAL_ARGS='-QueryPort=27015 -SteamServerName="MyServer"' \
  -v "$(pwd)/gamefiles:/server" \
  -p 7777:7777/udp -p 27015:27015/udp \
  servy-wine:latest
```

## Direct command mode

```bash
docker run --rm -v "$(pwd)/gamefiles:/data" -w /data servy-wine:latest \
  wine AbioticFactor/Binaries/Win64/AbioticFactorServer-Win64-Shipping.exe -log
```

Commands ending in `.exe/.bat/.cmd/.msi` are wrapped with `wine` automatically when passed as image arguments:

```bash
docker run --rm -v "$(pwd)/gamefiles:/data" -w /data servy-wine:latest \
  ./AbioticFactor/Binaries/Win64/AbioticFactorServer-Win64-Shipping.exe -log
```

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `APPID` | — | Steam AppID to install/update (env mode) |
| `RUN_CMD` | — | Server command line (env mode, required) |
| `INSTALL_DIR` | `/server` | Game install / data directory |
| `AUTO_UPDATE` | `false` | Run steamcmd update on every start |
| `STEAM_USER` | `anonymous` | Steam account (protected apps need a real one) |
| `STEAM_PASS` | — | Steam password |
| `STEAM_GUARD` | — | Steam Guard code (sent **before** `+login`) |
| `PLATFORM` | `windows` | Depot platform: `windows` fetches Windows builds |
| `STEAMCMD_ARGS` | — | Extra raw steamcmd arguments |
| `INSTALL_RETRIES` | `3` | Download attempts before failing |
| `ADDITIONAL_ARGS` | — | Extra args appended to `RUN_CMD` |
| `WINE_PREFIX` | `/home/gameserver/.wine` | Wine prefix location |
| `WINEARCH` | `win64` | `win64` / `win32` |
| `WINEDEBUG` | `-all` | Wine debug channels |
| `DISPLAY` | `:99` (Xvfb) | Use an existing X display instead of Xvfb |
| `SKIP_INSTALL` | `false` | Skip the steamcmd step entirely |

## How Servy uses this image

Windows-only games in the catalog (`internal/steamcmd/games/*.yaml`, `windows_only: true`) declare `docker_image:` pointing at this image. When a server with the docker execution backend starts, the panel:

1. binds the server directory (installed via containerized steamcmd) to the container work dir
2. publishes the catalog's `expose` ports
3. runs the game's execution command — the entrypoint detects `.exe`/`.bat` commands and runs them via `wine`
4. translates SIGTERM (docker stop) into `wineserver -k` for a clean exit

Override the image per server in the panel UI (server settings → docker image) if you build your own variant.