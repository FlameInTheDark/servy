# API reference

Servy's panel is a normal REST API served by the same process and the same port
as the web UI:

```
http://<host>:8443/api/v1/…        REST endpoints
ws://<host>:8443/ws                websocket (realtime events, console)
http://<host>:8443/status          public status (no auth)
http://<host>:8443/metrics         Prometheus metrics (no auth)
```

Requests and responses are JSON (`content-type: application/json`). There is no
API version negotiation beyond the `/api/v1` prefix.

> The panel has no TLS of its own — put it behind a reverse proxy if the API is
> reachable from untrusted networks. See
> [`install.md`](install.md#behind-a-reverse-proxy).

## Authentication

```bash
# 1. log in and keep the token
TOKEN=$(curl -s -X POST http://localhost:8443/api/v1/auth/login \
  -H 'content-type: application/json' \
  -d '{"username":"admin","password":"secret"}' | jq -r .data.token)

# 2. use it on every other call
curl -s -H "Authorization: Bearer $TOKEN" \
  http://localhost:8443/api/v1/servers
```

The token can be supplied in three ways, checked in this order:

| Way | When it is used |
| --- | --- |
| `?token=<token>` query parameter | Downloads and iframes where headers cannot be set |
| `Authorization: Bearer <token>` header | Scripts and API clients |
| `token` cookie | The web UI (set by `/auth/login` and `/auth/setup`) |

Tokens are per-session, expire after `cookie_expire` days (default 7), and are
invalidated by `/auth/logout`, `/auth/invalidate_tokens`,
`/users/{id}/invalidate_tokens`, a password change, or an account/role change.

Relevant auth endpoints:

| Method & path | Body | Notes |
| --- | --- | --- |
| `POST /api/v1/auth/login` | `{"username","password","totp_code?"}` | Returns `{token, user, servers}` and sets the cookie |
| `POST /api/v1/auth/setup` | `{"username","password","lang?"}` | First run only; creates the superuser and signs in |
| `POST /api/v1/auth/logout` | — | Clears the cookie |
| `POST /api/v1/auth/invalidate_tokens` | — | Kills every session of the caller |
| `POST /api/v1/auth/validate` | — | Cheap token check → `{valid, user_id, username}` |
| `GET /api/v1/setup/status` | — | Public: `{needs_setup, users}` — drive the setup wizard |

Login failures answer `401` with a code: `BAD_PASSWORD`, `MFA_REQUIRED` (send
`totp_code` and retry), `INVALID_MFA`, `LOCKED_OUT` (`429`, too many attempts),
`ACCOUNT_DISABLED` (`403`). The attempt limit is `max_login_attempts`
(default 3).

### API keys

```bash
curl -s -X POST http://localhost:8443/api/v1/users/1/key \
  -H "Authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d '{"name":"ci","full_access":false,
       "server_permissions":"00000100",
       "panel_permissions":"000"}'
# → data.token = "3.Kf9…"   (shown once, keep it)
```

Here `00000100` grants only `FILES` (the 6th bit) and `000` grants no
panel-wide rights, so the key can do file operations on servers its owner can
already access.

Send the key exactly like a session token (`Authorization: Bearer 3.Kf9…`). The
key's masks are intersected with the owning user's permissions:

- `full_access: true` — the key acts as its user, ignoring the masks;
- otherwise `server_permissions` / `panel_permissions` limit what the key can
  do. Mask letters: server permissions are
  `COMMANDS TERMINAL LOGS SCHEDULE BACKUP FILES CONFIG PLAYERS` in that order;
  panel permissions are `SERVER_CREATION USER_CONFIG ROLES_CONFIG`.

Keys are listed at `GET /users/{id}/key`, can be re-scoped with
`PATCH /users/{id}/key/{token_id}`, and deleted with `DELETE`. The secret is
never returned again after creation.

## Responses

Success:

```json
{ "status": "ok", "data": { "…": "…" } }
```

Failure:

```json
{ "status": "error", "error": "SERVER_NOT_FOUND", "error_data": "Server not found" }
```

`error` is a stable machine-readable code (switch on it), `error_data` is a
translated human message. Custom validation failures use `failMsg` and put the
same text in `error` and `error_data`.

Common codes:

| HTTP | Codes |
| --- | --- |
| 400 | `INVALID_JSON`, `INVALID_JSON_SCHEMA`, `INVALID_ACTION`, `TRAVERSAL DETECTED`, `ALREADY_EXISTS`, `MANIFEST_DOWNLOAD_FAILED` |
| 401 | `INVALID_TOKEN`, `BAD_PASSWORD`, `MFA_REQUIRED`, `INVALID_MFA` |
| 403 | `NOT_AUTHORIZED`, `ACCOUNT_DISABLED`, `LIMIT_REACHED` |
| 404 | `NOT_FOUND`, `SERVER_NOT_FOUND` |
| 409 | `STORAGE_IN_USE`, `PORT_CONFLICT` |
| 429 | `LOCKED_OUT` |
| 500/502 | `SERVER_ERROR`, `NODE_OFFLINE`, `NODE_ERROR`, `CLUSTER_DISABLED` |

Most endpoints do not paginate; they return every row the caller may see.
`GET /panel/audit` accepts `?limit=` (default 100) and `?offset=`.

## Permissions

Two independent models:

- **Panel permissions** (`server_creation`, `user_config`, `roles_config`) for
  panel-wide actions such as creating a server or importing a manifest.
  Superusers pass every check.
- **Per-server permissions** granted through roles:
  `COMMANDS` (start/stop/restart/kill/commands), `TERMINAL` (console stream),
  `LOGS`, `SCHEDULE` (tasks), `BACKUP` (backup configs and archives),
  `FILES`, `CONFIG` (server settings, config files, webhooks, updates and
  clones), `PLAYERS`.

Having *access* to a server at all is separate from these bits: roles grant both,
so removing the server from a role hides it completely.

Missing permission → `403 NOT_AUTHORIZED`. A user without access to a server
gets the same `403` for it, as if it did not exist. `GET /users/{id}/permissions`
returns the effective masks of a user.

Routes marked **superuser** below require an admin account.

## Endpoints

### Servers

| Method & path | Permission | Purpose |
| --- | --- | --- |
| `GET /servers` | access | List servers visible to the caller with runtime state |
| `POST /servers` | `SERVER_CREATION` | Create a server (wizard or manifest payload) |
| `POST /servers/stop_all` | superuser | Stop every local server |
| `GET /servers/status` | access | Compact running/stopped map |
| `GET /servers/{id}` | access | Full server view (config, runtime, node, game snapshot) |
| `PATCH /servers/{id}` | `CONFIG` | Patch name, ports, memory, docker settings, launch options, … |
| `DELETE /servers/{id}` | `CONFIG` | Delete the server (`?files=true` also removes its files) |
| `PATCH /servers/{id}/update/config` | `CONFIG` | Patch the editable creation config |
| `POST /servers/{id}/start\|stop\|restart\|kill` | `COMMANDS` | Lifecycle (backup-first rules apply to updates, not lifecycle) |
| `POST /servers/{id}/stdin` | `COMMANDS` | Send a console command: `{"command":"say hi"}` |
| `POST /servers/{id}/agree_eula` | `CONFIG` | Accept the Minecraft EULA |
| `POST /servers/{id}/update_executable` | `CONFIG` | Update the server (Minecraft/Bedrock/custom URL; not for `steam_cmd`) |
| `POST /servers/{id}/clone` | `CONFIG` | Clone the server under a new id |
| `GET /servers/{id}/stats` | access | Current CPU/RAM/players/disk |
| `GET /servers/{id}/history` | access | Downsampled stats history for charts |
| `GET /servers/{id}/logs` | `LOGS` | Latest log lines |
| `GET /servers/{id}/terminal` | `TERMINAL` | Console scrollback |
| `GET /servers/{id}/players` | `PLAYERS` | Player list (bedrock/steam games, when supported) |
| `GET /servers/{id}/public` | any authenticated user | Public status for share links/world widgets (404 unless the server has *show status* enabled) |
| `GET /servers/{id}/launch-options` | `CONFIG` | Editable launch options for the server's game manifest |
| `GET /servers/{id}/users` | access | Users with access to this server |
| `GET/PUT /servers/{id}/config` and `/config-file` / `/config/{file}` | `CONFIG` | Visual/raw config editor; nested paths use `?path=` or the path form |

Creation payload (`POST /servers`) — `create_type` decides which fields matter:

| `create_type` | Required | Optional |
| --- | --- | --- |
| `download_jar` | `name`, `jar_url` | `jar_name`, `java_selection`, `min_mem`, `max_mem` |
| `import_zip` | `name`, `archive_path` (upload first) | `executable`, `execution_command` |
| `import_jar` | `name`, `archive_path` (upload first) | `jar_name`/`executable`, `java_selection`, `min_mem`, `max_mem` |
| `raw_exec` | `name`, `executable`, `execution_command` | `shell` |
| `steam_game` | `name`, `steam_slug` or `app_id` | `port`, `steam_options` |

Common to all: `type`, `port`, `stop_command`, `roles[]`, `auto_start`,
`crash_detection`, `node`, `execution_backend` (`process`/`docker`),
`docker_image`, `docker_env`, `docker_ports`, `docker_binds`, `docker_network`,
`docker_workdir`, `docker_autoremove`. Archives are uploaded first:

```bash
curl -s -X POST http://localhost:8443/api/v1/servers/import/upload \
  -H "Authorization: Bearer $TOKEN" -F 'file=@server.zip'
# → data.path  (pass it as archive_path)
```

### Files

All file operations live under one path prefix and are selected by method and
`?action=`. The path after `/files/` is the server-relative path (URL-encoded).

| Request | Action |
| --- | --- |
| `GET /servers/{id}/files/?path=sub/dir` | List a directory |
| `GET /servers/{id}/files/{path}` | Read a text file (≤ 16 MiB) |
| `GET /servers/{id}/files/{path}?action=download` | Download (≤ 512 MiB) |
| `GET /servers/{id}/files/{path}?action=stat` | Stat one entry |
| `POST /servers/{id}/files/{path}?action=create[&kind=dir]` | Create file/directory |
| `POST /servers/{id}/files/{path}?action=write` | `{"content":"…"}` |
| `PUT/PATCH /servers/{id}/files/{path}` | Same as `action=write`, with audit logging |
| `POST /servers/{id}/files/{path}?action=move` / `?action=copy` | `{"dest":"other/path"}` |
| `POST /servers/{id}/files/{path}?action=zip` | `{"paths":["a","b"],"out":"archive.zip"}` |
| `POST /servers/{id}/files/{path}?action=unzip&dest=.` | Extract a zip |
| `POST /servers/{id}/files/{path}?action=upload&chunk=N&total=M&upload_id=X` | Chunked upload |
| `DELETE /servers/{id}/files/{path}` | Delete file or directory |

Every path is resolved inside the server directory; escapes answer
`400 TRAVERSAL DETECTED`. All routes here require the `FILES` permission.
`GET /servers/import/upload` (wizard archive upload) requires
`SERVER_CREATION` instead.

### Backups, schedules and webhooks

| Method & path | Permission | Purpose |
| --- | --- | --- |
| `GET/POST /servers/{id}/backups` | `BACKUP` | List / create backup configs |
| `GET /servers/{id}/backup-folders` | `BACKUP` | Folder picker data: manifest folders + directories in the server dir |
| `GET/PATCH/DELETE /backups/backup/{backup_id}` | `BACKUP` | Config detail, edit, delete (`?files=true` deletes archives too) |
| `POST /backups/backup/{backup_id}/run` | `BACKUP` | Run a backup now |
| `GET /backups/backup/{backup_id}/archives` | `BACKUP` | Archives with their location (host / storage) |
| `GET /backups/backup/{backup_id}/download/{name}` | `BACKUP` | Download an archive (fetches it back from storage if needed) |
| `GET /backups/backup/{backup_id}/files/{name}` | `BACKUP` | List the contents of an archive |
| `POST /backups/backup/{backup_id}/restore/{name}` | `BACKUP` | Restore an archive |
| `GET/POST /servers/{id}/tasks` | `SCHEDULE` | List / create schedules (tasks) |
| `GET/PATCH/DELETE /tasks/{task_id}` | `SCHEDULE` | Schedule detail, edit, delete |
| `GET /tasks/{task_id}/children` | `SCHEDULE` | Reaction children of a schedule |
| `POST /tasks/{task_id}/run` | `SCHEDULE` | Run a schedule now |
| `GET/POST /servers/{id}/webhook` | `CONFIG` | List / create webhooks |
| `GET/PATCH/DELETE /webhook/{webhook_id}` | `CONFIG` | Webhook detail, edit, delete |

Backup config bodies (`POST /servers/{id}/backups`):

```json
{
  "name": "nightly",
  "backup_type": "zip_vault",   // zip_vault | snapshot
  "included_dirs": ["saves"],   // folders to archive; [] = the whole server dir
  "excluded_dirs": ["logs"],
  "max_backups": 7,             // used when no storage destination is set
  "compress": true,
  "shutdown": true,             // stop the server while archiving
  "before": "save-all",         // console command before the backup
  "after": "",                  // console command after it
  "default": false,             // the "Default backup" of the server
  "enabled": true,
  "storage_id": "…"             // omit = default destination, "" = host only
}
```

On `PATCH /backups/backup/{id}`, `included_dirs` and `excluded_dirs` keep their
current value when the field is left out, and are cleared when an empty array is
sent. `GET /servers/{id}/backup-folders` answers what the tab's folder picker
shows: the folders the server's manifest declared (`manifest_dirs`, with
`manifest_source` naming the game or recipe) and the directories that exist in
the server directory right now (`dirs`).

Schedule bodies (`POST /servers/{id}/tasks`):

```json
{
  "name": "nightly restart",
  "action": "restart",          // start | stop | restart | kill | backup | command
  "interval": "days",           // minutes | hours | days | cron | reaction
  "interval_int": 1,            // for minutes/hours/days
  "cron_string": "0 4 * * *",   // for interval=cron
  "command": "save-all",        // for action=command
  "action_id": "",              // backup config id for action=backup (empty = default)
  "one_time": false,            // disable after the first run
  "parent": null,               // schedule id for reaction children
  "delay": 0,                   // seconds before a reaction child runs
  "comment": "",
  "enabled": true
}
```

Storage destinations are panel-wide (see [`backups.md`](backups.md) for the
retention model):

| Method & path | Permission | Purpose |
| --- | --- | --- |
| `GET /panel/backup-storages` | access | List destinations |
| `GET /panel/backup-storages/kinds` | access | Backends and their form fields |
| `GET /panel/backup-storages/{id}` | access | Read one (secrets masked) |
| `POST /panel/backup-storages` | superuser | Create |
| `PATCH /panel/backup-storages/{id}` | superuser | Update (send `********` to keep a secret) |
| `DELETE /panel/backup-storages/{id}[?force=true]` | superuser | Delete (`409 STORAGE_IN_USE` unless forced) |
| `POST /panel/backup-storages/test` | superuser | Test an unsaved destination |
| `POST /panel/backup-storages/{id}/test` | superuser | Test a saved destination |

### Users, roles and keys

| Method & path | Permission | Purpose |
| --- | --- | --- |
| `GET/POST /users` | superuser | List / create users |
| `GET/PATCH /users/{user_id}` | self or superuser | Read / update (roles, limits, language, order…) |
| `DELETE /users/{user_id}` | superuser | Delete |
| `POST /users/{user_id}/password` | self or superuser | Change password (invalidates sessions) |
| `POST /users/{user_id}/invalidate_tokens` | self or superuser | Kill sessions |
| `GET/POST /users/{user_id}/key` | self or superuser | List / create API keys |
| `GET/PATCH/DELETE /users/{user_id}/key/{token_id}` | self or superuser | Manage one key |
| `GET/POST /users/{user_id}/totp` | self or superuser | List / enrol TOTP (`POST` returns the secret + QR payload) |
| `POST /totp/{totp_id}/verify` | self or superuser | Confirm a code |
| `DELETE /totp/{totp_id}` | self or superuser | Remove a factor |
| `GET /totp/recovery/renew` | self or superuser | New recovery codes |
| `GET /users/{user_id}/permissions` | self or superuser | Effective masks |
| `GET /roles`, `GET /roles/{role_id}` | access | List / read roles |
| `POST /roles`, `PATCH/DELETE /roles/{role_id}` | superuser | Manage roles |
| `GET /roles/{role_id}/servers`, `/users` | access | Role members |

### Panel

| Method & path | Permission | Purpose |
| --- | --- | --- |
| `GET /panel/check` | public | Liveness + version |
| `GET /panel/stats` | access | Host CPU/RAM/disks/uptime |
| `GET /panel/config` | access | Panel settings (`GET`), grouped by category |
| `PATCH /panel/config` | superuser | Change settings live (verified by `restart: true` fields) |
| `GET /panel/logs/{type}` | superuser (or `general_user_log_access`) | `audit`, `session`, `schedule`, `cmd_queue`, `commander`, `auth`, `server` |
| `GET /panel/audit?limit=&offset=` | superuser | Audit trail |
| `GET /panel/support_logs`, `/status` | superuser | Download a support bundle (logs + config); progress arrives as `support_log_status` websocket events |
| `GET /panel/java`, `GET /panel/shells`, `GET /panel/docker` | access | Detected runtimes/shells/Docker status |
| `POST /panel/docker/images` | superuser | Pull an image |
| `GET /panel/SteamCache`, `GET /panel/JarCache` | access | Cache inventory (legacy aliases plus `SteamCatalog`) |
| `GET /panel/update_check` | superuser | Update probe result |
| `GET/POST /panel/announcements` | access | Panel announcements (public list under `/api/v1/public/announcements`) |
| `POST /panel/admin/upload` | superuser | Upload admin images (limit `max_image_upload_size_mb`) |
| `GET/POST /panel/antilockout` | superuser | Anti-lockout recovery configuration |

### Steam catalog, manifests, cluster nodes

| Method & path | Permission | Purpose |
| --- | --- | --- |
| `GET /panel/steam/catalog` | access | Built-in + custom games |
| `GET /panel/steam/catalog/custom` | superuser | Custom overlay entries |
| `POST /panel/steam/catalog/custom/validate` | superuser | Validate `{"manifest":"YAML/JSON"}` |
| `POST /panel/steam/catalog/custom` | superuser | Save/replace a custom game manifest |
| `DELETE /panel/steam/catalog/custom/{slug}` | superuser | Remove a custom entry |
| `GET /panel/manifests` | access | Stored server-creation recipes |
| `POST /panel/manifests` | superuser | Import `{"manifest":"…"}` or `{"url":"https://…"}` |
| `GET/DELETE /panel/manifests/{definition_id}` | access / superuser | Read / delete a recipe |
| `POST /panel/manifests/apply` | `SERVER_CREATION` | Create a server from `{"definition_id"}` or `{"url"}` |
| `GET /panel/nodes`, `GET /panel/nodes/{node_id}` | superuser | Cluster nodes |
| `POST /panel/nodes/{node_id}/ping`, `DELETE /panel/nodes/{node_id}` | superuser | Probe / remove a node |
| `GET/POST /panel/nodes/tickets` | superuser | List / mint join tickets |
| `DELETE /panel/nodes/tickets/{hash}` | superuser | Revoke a ticket |

Manifest formats, limits and every field are documented in
[`manifests.md`](manifests.md); the cluster flow in [`cluster.md`](cluster.md).

## Websocket

```
ws://<host>:8443/ws            (also /ws/)
```

Authentication happens during the upgrade: send the session token or API key
via the `token` cookie, `?token=` or an `Authorization` header. The connection
is closed with `401` otherwise. Every message is a JSON object:

```json
{ "type": "subscribe",   "topic": "server.<server-id>" }
{ "type": "unsubscribe", "topic": "server.<server-id>" }
{ "type": "ping" }

{ "type": "pong" }
{ "type": "event", "topic": "server.<id>", "event": "server_start", "data": { … } }
{ "type": "event", "topic": "server.<id>", "event": "error",
  "data": { "error": "NOT_AUTHORIZED" } }
```

| Topic | Receives | Permission |
| --- | --- | --- |
| `host` | Host stats, panel-wide updates | any authenticated user |
| `notifications` | Notifications addressed to the user | any authenticated user |
| `creation` | SteamCMD install output for *your* creation jobs | any authenticated user |
| `server.<id>` | Server lifecycle, stats, backups, config changes | access to that server |
| `server.<id>.terminal` | Live console output | `TERMINAL` permission |

Event names include `server_start`, `server_stop`, `server_restart`,
`server_kill`, `crash_detected`, `backup_start`, `backup_end` (`backup_server`
for webhooks), `vterm_new_line`, `send_start_reload`, `send_eula_bootbox`,
`notification`, `panel_config_update`, `host_stats`, `server_stats`,
`steamcmd_output`, `support_log_status`, `announcement`.

Console output arrives in batches (`vterm_new_line`), so a client should append
`data.lines` rather than reconnect. Slow clients are dropped rather than
blocking the panel (a 256-message buffer per connection).

## Public endpoints

| Endpoint | Purpose |
| --- | --- |
| `GET /status` | `{status, version, running_servers, time}` — no auth, for uptime widgets |
| `GET /metrics` | Prometheus text format: host CPU/RAM/disks plus per-server gauges |
| `GET /metrics/servers/{id}` | The same gauges filtered to one server |
| `GET /api/v1/public/announcements` | Announcements for the login screen |
| `GET /api/v1/setup/status` | Whether the setup wizard is still needed |
| `GET /api/v1/openapi.json` | A route inventory stub (not a full OpenAPI schema) |

Exported metrics:

```
servy_host_cpu_percent, servy_host_mem_percent, servy_host_mem_usage_bytes,
servy_host_mem_total_bytes, servy_host_disk_usage_bytes{mount=…},
servy_host_disk_total_bytes{mount=…},
servy_server_running{server_id=…},   servy_server_cpu_percent{server_id=…},
servy_server_mem_bytes{server_id=…}, servy_server_players_online{server_id=…},
servy_server_players_max{server_id=…}, servy_server_ping_ms{server_id=…},
servy_server_crashed{server_id=…},   servy_server_world_size_bytes{server_id=…}
```

## End-to-end example

```bash
BASE=http://localhost:8443/api/v1
TOKEN=$(curl -s -X POST $BASE/auth/login -H 'content-type: application/json' \
        -d '{"username":"admin","password":"secret"}' | jq -r .data.token)
AUTH="Authorization: Bearer $TOKEN"

# which servers exist and what are they doing?
curl -s -H "$AUTH" $BASE/servers | jq -r '.data[] | "\(.server_id) \(.server_name) \(.state)"'

# start one, send a command, watch the console for 10 s
ID=$(curl -s -H "$AUTH" $BASE/servers | jq -r '.data[0].server_id')
curl -s -X POST -H "$AUTH" $BASE/servers/$ID/start > /dev/null
curl -s -X POST -H "$AUTH" -H 'content-type: application/json' \
     -d '{"command":"say hello from the API"}' $BASE/servers/$ID/stdin > /dev/null

python3 - "$ID" "$TOKEN" <<'PY'
import json, sys, websocket          # pip install websocket-client
srv, token = sys.argv[1], sys.argv[2]
ws = websocket.create_connection(f"ws://localhost:8443/ws?token={token}")
ws.send(json.dumps({"type": "subscribe", "topic": f"server.{srv}.terminal"}))
ws.settimeout(10)
try:
    while True:
        print(json.loads(ws.recv()))
except Exception:
    pass
PY
```

## Error handling tips

- Always branch on the `error` code, not on `error_data` (which is translated).
- `403` on a server you can see in the list means the role lacks that specific
  permission bit; check `GET /users/{id}/permissions`.
- `502 NODE_OFFLINE` / `NODE_ERROR` mean the panel could not reach the node that
  owns the server — retry after checking `GET /panel/nodes`.
- Long-running operations (creation, updates, restores) report progress over the
  websocket; the HTTP call returns when the operation has been *accepted* or
  finished, depending on the endpoint.
