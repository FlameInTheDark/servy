# Cluster Setup

A Servy **panel** runs the web UI, the database and the schedules. A Servy
**node** is another machine that runs game servers on the panel's behalf: the
panel talks to it over gRPC, the node keeps its own console, files and backups
locally, and the UI shows its servers like any other.

One panel can drive any number of nodes. Nodes never need a public web port —
only the panel has to reach their node API port.

## 1. Enable the node API on the panel

`Nodes → Add node` needs the panel's gRPC listener, which is off by default.

In `Panel settings → Cluster` (or `config.yaml`) set:

```yaml
grpc_enabled: true
grpc_port: 5055      # this is the default; change it if the port is taken
```

Only the **panel** uses this port. Each node listens on its own node API port
(`--grpc-port`, default `50052`) which the panel dials.

Restart the panel. On boot it prints the address nodes should join:

```
Cluster node API on :5055 — add machines under Nodes → Add node
```

Then open `Nodes` in the UI. If the panel runs in Docker, publish this port
too — see [`docker.md`](docker.md#running-a-node-in-docker).

## 2. Add the node

1. Click **Add node**. The dialog shows a one-time command such as:

   ```
   servy agent run --panel panel.example.com:5055 --ticket 7f3c… --name build-box
   ```

   Copy it (or copy just the ticket).

2. On the new machine, install Servy and run that command. The agent joins once,
   stores its node credentials (`<data-dir>/config/agent.json`, mode `0600`) and
   keeps running:

   ```
   Joined as node "build-box" (n-9f21c4). Credentials stored in …
   Servy agent 3.6.0 — node n-9f21c4 (build-box)
   Node API on :50052 | panel panel.example.com:5055 | servers in /srv/servy/servers
   ```

3. The node appears in `Nodes` as soon as it registers — no second step in the
   UI. From then on start the agent with plain `servy agent run` (the panel
   address and token are remembered) and keep it running, e.g. under systemd or
   Docker with `restart: unless-stopped`.

Join tickets are single-use and expire after 10 minutes; the panel only keeps
their SHA-256 hash. Issuing a new one costs one click.

## 3. Run servers on the node

Create a server as usual (`Servers → Create server`) and pick the node in
**Where should it run?** on the first step. Everything after that is identical
to a local server:

| Area | Remote behaviour |
|------|------------------|
| Creation | `import_zip` / `import_jar` archives and downloaded jars are streamed to the node and unpacked there |
| Lifecycle | start, stop, restart, kill, `stop all`, auto-start on boot |
| Console | live terminal + command input (streamed from the node's process) |
| Files | browse, read, write, rename, delete, unzip, chunked uploads |
| Config | `server.properties`, JSON/YAML/properties editors |
| Backups | create, browse archives, download, restore — all executed by the node |
| Schedules & webhooks | the panel's scheduler triggers the node that owns the server |
| Monitoring | per-node CPU/RAM, per-server state, players, version |

Server rows keep living in the panel database so the dashboard, node filters and
the audit log stay in one place; the node owns the actual files and processes.

## Requirements and networking

- The panel must reach the node's node-API port (`--grpc-port`, default
  `50052`). Open it between the machines (VPN/private network recommended).
- The node needs no inbound web port and no panel database — it keeps its own
  small SQLite file, server directory, backups and logs.
- `--advertise` overrides the address the panel dials when the node's own idea
  of its IP is wrong (NAT, multiple interfaces).
- If the node's IP changes while the address port stays the same, the panel
  follows the address the node reports on its next hello.

## Security

- Each node gets its own random token at join time. The panel stores only its
  hash. Every RPC is authenticated with `authorization: Bearer <token>` plus the
  node id, and each node may only touch its own servers.
- Removing a node in the UI (`Nodes → ⋯ → Remove`) revokes it: the node is
  refused on the next RPC. Servers it hosts are protected — the panel answers
  `NODE_HAS_SERVERS` and lists them, so you can move or delete them first, or
  force the removal.
- Re-installing or re-joining a node (new ticket) invalidates the old
  credentials; the node name is kept.

## CLI

```bash
servy agent run [--panel HOST:PORT] [--ticket T] [--name NAME] [--advertise ADDR] [--grpc-port 50052]
servy agent status          # node id, name, panel, join time
servy agent leave           # forget credentials (a new ticket is needed to re-join)
servy node list             # servers this machine runs for the panel
servy node whoami           # node identity as the panel knows it
```

Nodes use the same binary as the panel — the agent is just a CLI mode, so
`servy --agent run …`, `servy --agent status` and `servy --agent leave` are
equivalent to the `servy agent …` commands above.

## Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| `Nodes → Add node` says clustering is disabled | `grpc_enabled` is false, or the panel was not restarted |
| Agent: `this node has not joined a panel yet` | first run needs `--panel` **and** `--ticket` |
| Agent: `ticket rejected` | the ticket is older than 10 minutes or already used — issue a new one |
| Node shows **offline** | the agent is not running, or the panel cannot dial its `grpc-port` (firewall, wrong `--advertise`) |
| Node shows offline after an IP change | the panel follows reported addresses on the next hello; check `--advertise` if it does not recover |
| `NODE_OFFLINE` when starting a server | the node answered nothing in time — check the agent logs on that machine |
