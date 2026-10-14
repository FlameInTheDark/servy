# Docker deployment

Servy runs as a container in one of two roles:

| Role | Image | What it does |
| --- | --- | --- |
| **Panel** | `ghcr.io/flameinthedark/servy:latest` | Web UI, API, websocket, scheduler, local game servers, cluster control |
| **Node** | same image, different command | Runs game servers for a panel on another machine — see [`cluster.md`](cluster.md) |
| **Windows games** | `ghcr.io/flameinthedark/servy-wine:latest` | Wine + SteamCMD base image, used *by* the panel for Windows-only servers |

The panel image is based on `debian:bookworm-slim` and already contains:

- Temurin JREs **8, 11, 17, 21** (`/opt/java/<version>`, with `16 → 17` and
  `25 → 21` symlinks for Minecraft version selection);
- **SteamCMD** in `/opt/steamcmd` (amd64 only — it needs 32-bit libraries);
- `tini` as PID 1, `curl`, `unzip`, `wget`, `procps`, `gpg`;
- the `servy` binary (panel + agent), running as user `servy` (uid 1000) with
  `HOME=/data` and `VOLUME /data`.

It deliberately contains **no Docker CLI**: the panel talks to the Docker Engine
API directly over the mounted `/var/run/docker.sock`.

---

## Quick start (compose)

The repository ships a ready `docker-compose.yml`:

```bash
git clone https://github.com/FlameInTheDark/servy.git
cd servy
mkdir -p data
docker compose up -d
```

```
→ panel: http://localhost:8443  (plain HTTP; setup wizard on first boot)
```

That file mounts:

| Mount | Why it is required |
| --- | --- |
| `./data:/data` | The data directory: config, database, servers, backups, logs |
| `/var/run/docker.sock` | So the panel can spawn sibling containers (SteamCMD install jobs, Docker-execution servers, Wine servers) |

and sets `SERVY_HOST_DATA_PATH=${PWD:-.}/data` — the panel needs to know the
**host** path of its data directory, because sibling containers bind-mount
server folders from the host, not from inside the panel container. If that value
is wrong, containerized servers fail with a bind-mount error and SteamCMD
installs cannot see their install directory.

### Container user and file ownership

Servers that run in sibling containers are started with the panel's own
`uid:gid`, so the game can write into the bind-mounted server directory. To
override that per server, add `SERVY_DOCKER_USER` to the server's Docker
environment (server settings → Docker env, or `docker_env` in a recipe):

| Value | Effect |
| --- | --- |
| *unset* | Run as the panel's uid:gid (recommended) |
| `image` | Keep the image's own default user |
| `1000` or `1000:1000` | Run as that uid/gid |

The variable is consumed by the panel and never passed into the container.

### Ports

| Port | Protocol | Purpose |
| --- | --- | --- |
| `8443` | tcp | Web UI, REST API, websocket |
| `5055` | tcp | Cluster node API (`grpc_port`) — only after enabling it in Panel settings |
| `19132` | udp | Minecraft Bedrock default, when it runs inside the panel container |
| `25500-25600` | tcp + udp | Default game port range, for servers running inside the panel container |
| extras | — | Add a mapping for every game port your servers use (e.g. `2456-2457/udp` for Valheim) |

Containerized game servers publish their own ports through the Docker engine,
so **no host port mapping is needed for those** — but servers running in the
panel container itself (process backend) need the port published, and every
port must be open in the host firewall.

### Environment

| Variable | Default | Purpose |
| --- | --- | --- |
| `TZ` | `Etc/UTC` | Timezone for logs and schedules |
| `SERVY_HOST_DATA_PATH` | — | Host path of `/data` (see above) |
| `SERVY_DATA_DIR` | *unset* | Other way to point commands at `/data`; the default command already passes `--data /data` |
| `SERVY_UPDATE_CHECK_URL` | release API | `disabled` turns off update probes |
| `SERVY_STEAM_WINE_IMAGE` | `ghcr.io/flameinthedark/servy-wine:latest` | Override the Windows-game image |

### docker run without compose

```bash
docker run -d --name servy \
  --restart unless-stopped \
  -p 8443:8443 -p 25500-25600:25500-25600 -p 25500-25600:25500-25600/udp \
  -v "$PWD/data:/data" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -e SERVY_HOST_DATA_PATH="$PWD/data" \
  -e TZ=Etc/UTC \
  ghcr.io/flameinthedark/servy:latest
```

The entrypoint is `tini -- servy` and the default command is
`--data /data serve`, so the container behaves like `servy serve`.

The image has a healthcheck: `curl -fsS http://127.0.0.1:8443/status` every
30 s. `docker compose ps` (or `docker inspect --format '{{.State.Health.Status}}' servy`)
shows it.

## First run

Open `http://<host>:8443` to get the setup wizard, or create the admin from
inside the container:

```bash
docker compose exec servy servy --data /data setup -u admin -p 'change-me-please'
docker compose exec servy servy --data /data user list
```

## Windows-only game servers (Wine image)

Steam has no Linux build for some titles (Abiotic Factor, V Rising, ARK:
Survival Ascended, Enshrouded, …). Servy runs them in a sibling container built
from `docker/steamcmd-wine`, which provides SteamCMD + Wine + Xvfb and a
pre-initialized Wine prefix.

```bash
docker compose --profile wine build servy-wine    # or: make docker-wine
docker tag servy-wine:latest ghcr.io/flameinthedark/servy-wine:latest
```

Tagging it `ghcr.io/flameinthedark/servy-wine:latest` makes the built-in catalog
pick it up automatically; otherwise set `SERVY_STEAM_WINE_IMAGE` (or the
per-server docker image in the UI). The image's own environment variables
(`APPID`, `RUN_CMD`, `AUTO_UPDATE`, `WINE_PREFIX`, `INSTALL_DIR`, …) are
documented in
[`docker/steamcmd-wine/README.md`](../docker/steamcmd-wine/README.md) — that
file is also the reference for building the image with a matching uid/gid.

## Building your own panel image

```bash
make docker                 # → servy:dev
# or
docker build -t servy:dev \
  --build-arg VERSION=$(git describe --tags --always) \
  --build-arg COMMIT=$(git rev-parse --short HEAD) \
  --build-arg BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  .
```

Useful build arguments:

| Argument | Default | Meaning |
| --- | --- | --- |
| `JAVA_VERSIONS` | `"8 11 17 21"` | Space-separated JRE majors to bake in, e.g. `--build-arg JAVA_VERSIONS="17 21"` for a smaller image |
| `VERSION`, `COMMIT`, `BUILD_DATE` | `dev` / `unknown` / `unknown` | Stamped into `servy version` and the OCI labels |

The build has three stages: a Vite frontend build, a static Go build (panel +
agent, SPA embedded), and the Debian runtime described above. CI publishes
`ghcr.io/flameinthedark/servy` and `ghcr.io/flameinthedark/servy-wine` on
releases.

## Running a node in Docker

A node is the same image with a different command. It needs the panel's node
API to be reachable, its own game ports, and the Docker socket if its servers
should use the Docker backend:

```yaml
services:
  servy-node:
    image: ghcr.io/flameinthedark/servy:latest
    container_name: servy-node
    restart: unless-stopped
    command: ["--data", "/data", "agent", "run", "--panel", "panel.example.org:5055", "--ticket", "<ticket>", "--grpc-port", "50052", "--advertise", "node1.example.org:50052"]
    ports:
      - "50052:50052"                      # the panel dials this
      - "25500-25600:25500-25600"
      - "25500-25600:25500-25600/udp"
    volumes:
      - ./node-data:/data
      - /var/run/docker.sock:/var/run/docker.sock
    environment:
      SERVY_HOST_DATA_PATH: ${PWD:-.}/node-data
```

After the first join the ticket is consumed and the container can run
`servy --data /data agent run --grpc-port 50052` on its next start — the panel
address and credentials are remembered in `<data>/config/agent.json`.
`--advertise` must be the address **the panel** can dial, not the container's
internal name.

## Upgrades

```bash
docker compose pull
docker compose up -d
```

Migrations run automatically on startup; the data volume is untouched. Keep the
data directory on a real host path (not an anonymous volume) so a recreated
container still finds its servers, and so sibling containers can bind-mount
server folders.

## Backups of the panel itself

- `data/config/` contains `config.yaml`, the SQLite database and
  `steamgames.json` — the irreplaceable part.
- `data/servers/` and `data/backups/` are the bulky parts; back up game data
  through Servy's own backup schedules and keep the whole volume out of your
  container-image backups.
- Stopping the panel before copying gives a consistent database; with WAL mode a
  copy while running is usually fine but may need its `-wal`/`-shm` files
  alongside it.

## Troubleshooting

| Symptom | Cause / fix |
| --- | --- |
| `permission denied` on `/var/run/docker.sock` | The container user (uid 1000) is not allowed to use the socket. Run the container with a matching group (`group_add: ["<docker-gid>"]`) or with `--user root` on trusted hosts |
| Containerized server fails with *bind mount … no such file or directory* | `SERVY_HOST_DATA_PATH` is unset or wrong — it must be the host path of the `/data` mount |
| SteamCMD install cannot see the install directory | Sibling containers bind-mount the host data path; fix `SERVY_HOST_DATA_PATH` (and make sure the data dir is a bind mount, not an anonymous volume) |
| Server files owned by uid 1000 but host user differs | Rebuild the Wine image with `GAMESERVER_UID/GID`, or chown the data directory to uid 1000 |
| Health check flapping on slow machines | The probe has a 15 s start period; on very slow disks raise it with `start_period` or ignore it — it does not affect the panel |
| Panel reachable on 8443 but websocket does not connect | A proxy in front of the container is dropping the `Upgrade` header — see the nginx snippet in [`install.md`](install.md#behind-a-reverse-proxy) |
