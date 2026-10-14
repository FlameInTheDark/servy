# Installation & configuration

Servy is a single binary. It contains the web panel, the REST API, the
websocket server, the scheduler and the game-server engine. Nothing else has to
be installed to run Minecraft Java/Bedrock, Hytale, custom executables or
SteamCMD games — except **Java** for Minecraft servers and **Docker** if you
want servers to run in containers.

There are three ways to get it:

| Way | Best for |
| --- | --- |
| [Prebuilt release](#1-prebuilt-release) | Normal installs on Linux, macOS, Windows |
| [Build from source](#2-build-from-source) | Development, custom builds |
| [Docker](#3-docker) | Running the panel in a container (see [`docs/docker.md`](docker.md)) |

---

## 1. Prebuilt release

Every tagged release publishes archives for `linux/amd64`, `linux/arm64`,
`darwin/amd64`, `darwin/arm64` and `windows/amd64`, plus a `SHA256SUMS` file.

```bash
# pick the archive for your platform from the Releases page, then:
tar -xzf servy_1.2.3_linux_amd64.tar.gz
sudo install -m 0755 servy_1.2.3_linux_amd64/servy /usr/local/bin/servy
sudo install -m 0755 servy_1.2.3_linux_amd64/servy-agent /usr/local/bin/servy-agent

servy version        # servy 1.2.3 (commit …, built …)
```

Archives contain two binaries:

| Binary | What it is |
| --- | --- |
| `servy` | The panel **and** the node agent (`servy agent …` subcommands) |
| `servy-agent` | Tiny wrapper that simply calls `servy agent`, for hosts where only the agent should exist |

`servy` works on Windows too (`servy.exe`); put it anywhere on your `PATH`.

## 2. Build from source

Requirements: **Go 1.26+** and **Node 22+** (the frontend is built first and
embedded into the binary with `go:embed`).

```bash
git clone https://github.com/FlameInTheDark/servy.git
cd servy
make build          # frontend + backend → bin/servy, bin/servy-agent
```

Handy targets:

| Command | Result |
| --- | --- |
| `make build` | Frontend + both binaries in `bin/` |
| `make api` | Backend only, with a placeholder UI (API + websocket still work) |
| `make test` | `go test ./...` |
| `make release VERSION=v1.2.3` | Cross-platform archives in `dist/` |
| `make docker` | Panel image `servy:dev` (see [`docker.md`](docker.md)) |
| `make docker-wine` | Wine + SteamCMD image for Windows-only games |

## 3. Docker

The published image already ships the Java runtimes and SteamCMD. See
[`docs/docker.md`](docker.md) for compose files, volumes and environment
variables.

---

## First start

```bash
sudo mkdir -p /srv/servy
servy --data /srv/servy serve
```

```
Servy 1.2.3 — panel listening on http://0.0.0.0:8443
Data dir: /srv/servy | API: http://0.0.0.0:8443/api/v1 | WS: ws://0.0.0.0:8443/ws
```

Open `http://<host>:8443` and the setup wizard asks for the first admin
account. The same can be done without the browser:

```bash
servy --data /srv/servy setup -u admin -p 'change-me-please'   # flags are optional, it prompts
# or, while the panel runs:
curl -X POST http://localhost:8443/api/v1/auth/setup \
     -H 'content-type: application/json' \
     -d '{"username":"admin","password":"change-me-please"}'
```

`servy serve` flags:

| Flag | Meaning |
| --- | --- |
| `--data DIR`, `-d DIR` | Data directory. Defaults to `$SERVY_DATA_DIR`, then the current working directory |
| `--port N` | Override `https_port` for this run only |
| `--no-autostart` | Do not start servers that are marked auto-start |

Servers marked *auto-start* are started when the panel comes up. `--no-autostart`
is useful when you are upgrading or repairing something and do not want games
to boot yet.

> The panel speaks **plain HTTP**. Put it behind a reverse proxy for TLS, or
> keep it on a private network/VPN. The `https_port` key name is historical —
> it is simply the TCP port the panel listens on.

## Data directory

Everything Servy owns lives in the data directory. Keep it on a disk with room
for game servers and backups.

```
<data>/
├── config/
│   ├── config.yaml          # panel settings (mode 0600, written on first start)
│   ├── db/servy.sqlite      # users, servers, roles, schedules, backups, webhooks
│   ├── steamgames.json      # custom SteamCMD game manifests (overlay)
│   ├── bigbucket/           # download-manifest cache
│   ├── uploads/             # admin-uploaded images
│   └── agent.json           # node credentials (nodes only, mode 0600)
├── servers/<server-id>/     # each game server's files
├── backups/                 # local backup archives
├── logs/                    # panel logs
├── steamcmd/                # managed SteamCMD copy (if one is downloaded)
└── import/upload/           # wizard archives / staged manifest files
```

Back up the panel itself by copying `config/` (that includes the SQLite
database). `servers/` and `backups/` are the bulky parts and can be excluded if
the game data is backed up elsewhere.

## Configuration

`config/config.yaml` is created with defaults on first start and is rewritten
whenever a setting changes. The most useful keys:

| Key | Default | Meaning |
| --- | --- | --- |
| `http_host` | `0.0.0.0` | Interface the panel binds to (`127.0.0.1` to keep it local-only) |
| `https_port` | `8443` | Panel port (HTTP, API, websocket) |
| `grpc_enabled` | `false` | Enable the node API so other machines can join as cluster nodes |
| `grpc_port` | `5055` | Port of that node API |
| `cluster_id` | generated on first start | Namespace (`<prefix>/<cluster-id>/<server-id>/…`) for archives uploaded to a shared backup destination |
| `cookie_expire` | `7` | Login session lifetime in days |
| `max_login_attempts` | `3` | Failed logins per user before a temporary lockout |
| `super_mfa` | `false` | Require TOTP for superusers |
| `enable_otp_skew` | `true` | Accept TOTP codes from the neighbouring 30 s window |
| `general_user_log_access` | `false` | Let non-admin users read the panel log streams |
| `reset_secrets_on_next_boot` | `false` | One-shot: rotate the session signing secret on the next start (signs everyone out), then clear the flag |
| `max_log_lines` | `1000` | Per-log ring buffer size |
| `virtual_terminal_lines` | `200` | Console scrollback kept per server |
| `panel_logs_delete_after_days` | `0` | Delete panel logs older than N days (`0` keeps everything) |
| `stats_update_frequency_seconds` | `10` | How often host/server stats are sampled |
| `history_max_age` | `7` | Days of stats history kept for charts |
| `dir_size_poll_freq_minutes` | `5` | How often server directory sizes are recalculated |
| `monitored_mounts` | `["/"]` | Filesystems shown in the dashboard disk widget |
| `java_extra_paths` | `[]` | Extra directories scanned for Java installations |
| `big_bucket_repo` | `https://jars.arcadiatech.org` | Jar/download manifest source used by the Minecraft wizard |
| `docker_steam_image` | `steamcmd/steamcmd:latest` | Image used for SteamCMD install/update jobs |
| `backup_local_keep` | `1` | Local backup archives kept per server when a storage destination is used (`0` = keep all) |
| `backup_remote_keep` | `5` | Archives kept in the destination (`0` = keep all) |
| `max_image_upload_size_mb` | `5` | Upload limit for admin images |
| `terminal_keywords` | `ERROR, WARN, FATAL, SEVERE` | Words highlighted in the console |
| `sampling_tiers` | 6 h/1×, 24 h/2×, 72 h/6× | Chart downsampling tiers; `sampling_fallback_divisor` covers older windows |

Everything in the **Panel settings** page lives in this file, but only the keys
listed in the UI's groups are editable there. Any other key can be set by
editing `config.yaml` and restarting. Invalid values are clamped back to safe
defaults on load, and a malformed YAML file makes `servy serve` fail with the
parse error instead of silently starting with wrong settings.

### Environment variables

| Variable | Used by | Meaning |
| --- | --- | --- |
| `SERVY_DATA_DIR` | all commands | Data directory when `--data` is not given |
| `SERVY_HOST_DATA_PATH` | panel | Real host path of the data directory; needed when the panel runs in a container so sibling containers can bind-mount server folders |
| `SERVY_STEAM_WINE_IMAGE` | panel | Override the default Windows-game image (`ghcr.io/flameinthedark/servy-wine:latest`) |
| `SERVY_UPDATE_CHECK_URL` | panel | Update-check API endpoint; `disabled` turns the update probe off |

## Run as a service (systemd)

```ini
# /etc/systemd/system/servy.service
[Unit]
Description=Servy game server panel
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
User=servy
WorkingDirectory=/srv/servy
ExecStart=/usr/local/bin/servy --data /srv/servy serve
Restart=on-failure
RestartSec=5
# optional hardening
NoNewPrivileges=true
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now servy
journalctl -u servy -f
```

Run it under the same user that may write the data directory, and add that user
to the `docker` group if the Docker execution backend is used.
`servy serve` stays in the foreground on purpose — the process manager owns
daemonisation.

## Behind a reverse proxy

The panel serves plain HTTP, including the websocket at `/ws`. A minimal nginx
site:

```nginx
server {
    listen 443 ssl;
    server_name games.example.org;

    ssl_certificate     /etc/letsencrypt/live/games.example.org/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/games.example.org/privkey.pem;

    client_max_body_size 2g;          # server archive / world uploads

    location / {
        proxy_pass http://127.0.0.1:8443;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;      # websocket
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_read_timeout 3600s;                    # long console/download streams
        proxy_send_timeout 3600s;
    }
}
```

Long timeouts matter: the console stream and file downloads can stay open for
hours.

## Upgrading

1. Stop the panel (`systemctl stop servy`) — running game servers are stopped
   with it (they are supervised child processes, or containers the panel owns).
2. Replace the `servy` / `servy-agent` binaries.
3. Start the panel again. Database migrations run automatically on startup.

`config.yaml` and the data directory are never touched by an upgrade. After
upgrading, the UI shows the new version under **Panel settings → About**; a
built-in update check compares against the release API unless
`SERVY_UPDATE_CHECK_URL=disabled`.

## Command-line reference

Every command accepts the global `--data DIR` / `-d DIR` flag (or
`SERVY_DATA_DIR`) and must point at the same directory the panel uses.

| Command | Purpose |
| --- | --- |
| `servy serve [--port N] [--no-autostart]` | Run the panel |
| `servy setup [-u USER] [-p PASS] [--lang en_EN]` | Create the first admin user |
| `servy user add -u USER -p PASS [-s]` | Add a user (`-s` = superuser) |
| `servy user list` | List users |
| `servy user passwd -u USER -p PASS` | Change a password (invalidates sessions) |
| `servy migrate status` | Show the database location and migration state |
| `servy import --db PATH [--config-dir DIR]` | Import a legacy panel installation |
| `servy agent run [--panel HOST:PORT] [--ticket T] [--name N] [--advertise A] [--grpc-port 50052]` | Join/run as a cluster node — see [`cluster.md`](cluster.md) |
| `servy agent status` / `servy agent leave` | Node credentials state / forget them |
| `servy node list` / `servy node whoami` | Servers this machine runs / node identity |
| `servy manifest list\|add --file\|--url` | Server-creation recipes — see [`manifests.md`](manifests.md) |
| `servy game-manifest list\|validate\|add\|remove` | SteamCMD game catalog — see [`steamcmd.md`](steamcmd.md) |
| `servy version` | Version, commit and build date |

## Troubleshooting

| Symptom | Cause / fix |
| --- | --- |
| `listen tcp 0.0.0.0:8443: bind: address already in use` | Another process uses the port — change `https_port` or stop it |
| Browser shows the setup wizard but `setup` says *panel already initialized* | Users already exist; log in instead, or use `servy user passwd` to reset a password |
| Minecraft server fails with *Java not found* | Install a JRE (8, 11, 17 or 21 — one per Minecraft generation) or use the Docker execution backend, where the panel ships the runtimes |
| SteamCMD install fails | Only needed for servers running **directly on the host**. Either install Docker (recommended, the panel pulls `steamcmd/steamcmd`) or let Servy download SteamCMD into `<data>/steamcmd` |
| Files owned by the wrong user | Run the panel as the owner of the data directory; servers and uploads are written as the panel's user |
| Browser warns about an insecure connection | Expected: the panel is plain HTTP. Terminate TLS in a reverse proxy (see above) or tunnel it (VPN/SSH port forward) |
