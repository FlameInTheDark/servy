# Servy

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white" alt="Go Version" />
  <img src="https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black" alt="React" />
  <img src="https://img.shields.io/badge/License-MIT-green" alt="License" />
</p>

<p align="center">
  <strong>A modern, self-hosted game server management panel — single binary, zero external dependencies.</strong>
</p>

---

## Quick Start

### Binary (Recommended)

```bash
# Download latest release from GitHub Releases
# or build yourself:
make build

# First run — web setup wizard at http://localhost:8443
./bin/servy serve

# Or create admin via CLI and start:
./bin/servy setup -u admin -p supersecret
./bin/servy serve
```

Open **http://localhost:8443** → follow the wizard → create a server (zip import, jar download, SteamCMD game, or custom) → play.

### Docker

```bash
docker compose up -d
```

The panel image (`ghcr.io/flameinthedark/servy`) includes all Java runtimes (8/11/17/21) and SteamCMD.
Mount `/var/run/docker.sock` to let Servy spawn sibling containers for SteamCMD games.

> **Windows-only games** (Abiotic Factor, V Rising, etc.) use the companion `servy-wine` image (Wine + SteamCMD).
> See `docker/steamcmd-wine/README.md` for details.

---

## Features

- **Universal Game Support** — Minecraft (Java/Bedrock), SteamCMD titles (Valheim, CS2, Rust, Palworld, ARK, V Rising, Abiotic Factor, 7DTD, etc.), and arbitrary executables
- **Docker Execution** — Run any server in a container: images, ports, env, binds, networks, auto-remove; console + stdin + stats via `docker attach`
- **Java Version Manager** — Auto-detects JVMs (8/11/17/21/25), per-server selection, MC version to Java matrix hints; Docker image ships all runtimes
- **Real-time Terminal** — xterm.js over WebSocket, ANSI color, command queue, keyword highlights, multiple shell choices (bash, sh, cmd, pwsh, git-bash)
- **File Manager** — Browse, edit, upload, zip/unzip, download — traversal-proof PathGuard on every path
- **Backups** — Zip vault (compressed, retention) + snapshots (deduplicated), pre/post hooks, stop-before-backup, restore and browse
- **Schedules** — Interval, cron, daily, one-shot; reaction children on start/stop/crash/backup with delays
- **Monitoring** — Host + per-server stats (adaptive sampling), Java/Bedrock pings, player tracking, Prometheus `/metrics`
- **Webhooks** — Discord, Slack, Mattermost, Teams, Generic — templated bodies, per-event triggers
- **Auth & RBAC** — Argon2id, JWT sessions, TOTP 2FA + recovery codes, API keys (hashed at rest), 8-bit permission masks, quantity limits
- **Cluster & Agents** — Proxmox-style multi-node: register headless agents (`servy-agent`), manage remote servers from any panel, mTLS-ready
- **Config Editors** — Visual editor for `server.properties` (grouped schema), JSON with live validation, YAML/properties/text raw mode
- **Manifests** — URL-importable server definitions (`servy manifest add --url ...`) — marketplace-ready


---

## CLI Reference

```bash
servy serve [--data DIR]       # Run the panel (API + WS + UI + engine)
servy setup [-u -p]            # Create the first admin user
servy user add|list|passwd     # User management
servy agent --panel HOST:PORT  # Run as headless cluster agent
servy manifest list|add        # Manage server-definition manifests
servy node hello --addr ADDR   # Test cluster connectivity
servy migrate status           # Database migration status
servy version                  # Build info (version, commit, date)
```

---

## Project Layout

```
cmd/servy/           # Panel binary (API + WS + UI + engine)
cmd/servy-agent/     # Headless agent binary
internal/            # 28 Go packages (httpapi, servers, dockerx, backups, cluster, ...)
frontend/            # React 19 + Vite + TS + Tailwind + shadcn-style UI (dark)
proto/               # gRPC node service contract
queries/ + sqlc.yaml # SQL to typed Go (sqlc)
deploy/              # systemd unit + Caddy examples
Dockerfile           # Multi-stage; debian:slim runtime w/ Java 8/11/17/21 + steamcmd
docker/steamcmd-wine/# Wine + SteamCMD base image for Windows-only games
```

---

## Documentation

| Topic | Link |
|-------|------|
| Installation & Config | [`docs/install.md`](docs/install.md) |
| Docker Deployment | [`docs/docker.md`](docs/docker.md) |
| Cluster Setup | [`docs/cluster.md`](docs/cluster.md) |
| SteamCMD Games | [`docs/steamcmd.md`](docs/steamcmd.md) |
| Backup Strategies | [`docs/backups.md`](docs/backups.md) |
| API Reference | [`docs/api.md`](docs/api.md) |
| Manifests & Marketplace | [`docs/manifests.md`](docs/manifests.md) |

---

## Contributing

1. Fork the repo
2. Create a feature branch (`git checkout -b feat/amazing-feature`)
3. Commit with conventional commits (`feat: add amazing feature`)
4. Push and open a PR

All PRs run the full test suite (`go test ./...`) including E2E tests.

---

## License

MIT License — see [`LICENSE`](LICENSE) for details.

---

## Credits

- **shadcn/ui** — UI component inspiration
- **xterm.js** — Terminal rendering
- **Fiber, gRPC, sqlc, golang-migrate** — Core Go ecosystem
- **Community** — Game configs, testing, feedback

---

<p align="center">
  <strong>Made for the self-hosting community</strong>
</p>

<p align="center">
  <a href="https://github.com/FlameInTheDark/servy/issues">Report Bug</a> ·
  <a href="https://github.com/FlameInTheDark/servy/issues">Request Feature</a> ·
  <a href="https://github.com/FlameInTheDark/servy/discussions">Discussions</a>
</p>