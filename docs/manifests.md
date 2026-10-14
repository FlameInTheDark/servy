# Manifests

Servy has **two different manifest systems**. They solve different problems and
are not interchangeable:

| Manifest | Describes | Stored in | When it takes effect |
| --- | --- | --- | --- |
| **SteamCMD game manifest** ([reference](#steamcmd-game-manifests)) | A Steam app: how to download, launch, expose and configure it | Built-ins in the binary; custom ones in `<data>/config/steamgames.json` | Appears immediately in the game picker of **Create server**; used when a new server is installed |
| **Server-creation recipe** ([reference](#server-creation-recipes)) | A reusable "create a server like this" recipe: download a JAR, import a ZIP, install a Steam game, run a custom executable | Servy's database (`definitions` table) | When a user applies the recipe |

Both are **templates, not live patches**: they create servers, they never
rewrite servers that already exist. A created Steam server keeps a frozen copy
("snapshot") of the game manifest and the launch values that were used, so
editing the catalog later affects future servers only. To change a deployed
server, edit its real files under **Server → Files / Config** and restart it.

---

# SteamCMD game manifests

One file per game. It tells Servy which Steam app ID to install, what to run,
which ports to publish, which config files exist and which typed fields the
creation wizard should show.

## Where they live

| Source | Location | Notes |
| --- | --- | --- |
| Built-in | `internal/steamcmd/games/*.yaml` (40 games) | Embedded in the binary, see [`steamcmd.md`](steamcmd.md#built-in-catalog) |
| Custom | `<data>/config/steamgames.json` | Written by **Game manifests** in the UI or `servy game-manifest add`; overrides a built-in entry with the same slug **only when the app ID matches** |

The parser accepts **YAML or JSON** (same field names), rejects unknown keys,
and limits a manifest to **1 MiB** and one document. Validation happens before
anything is saved or installed.

## Minimal manifest

```yaml
app_id: 896660
slug: valheim
name: Valheim
executable: valheim_server.x86_64
run_command: ./valheim_server.x86_64 -name {SERVER_NAME} -port {PORT} -world {SAVE_DIR}/world
port: 2456
```

Six fields are required: `app_id`, `slug`, `name`, `executable`,
`run_command` and `port`. Everything else is optional.

## Field reference

| Field | Type | Required | Default | Meaning |
| --- | --- | --- | --- | --- |
| `app_id` | int | ✅ | — | Steam application ID to install (`1 … 2147483647`). Must be unique in the active catalog |
| `slug` | string | ✅ | — | Catalog key used in URLs, the CLI and `steam_slug` (`1–64` chars: lowercase letters, digits, hyphens; must start and end with a letter/digit) |
| `name` | string | ✅ | — | Display name in the game picker (`≤ 120` chars) |
| `executable` | string | ✅ | — | File that must exist inside the server directory. A start on the process backend fails with `EXECUTABLE_MISSING` when it is absent (Docker servers only need it inside the image) |
| `run_command` | string | ✅ | — | Launch command **template**. Placeholders are substituted and each `launch_options` value is appended as its own argv element — it is never passed to a shell |
| `port` | int | ✅ | — | Default game port offered in the wizard (`1 … 65535`) |
| `type` | string | — | forced `steam_cmd` | Accepted for compatibility; the parser always sets `steam_cmd` |
| `port_name` | string | — | `game` | Human label for the port field ("Game port", "Beacon port", …) |
| `query_port` | int | — | `port + 1` | Steam query (A2S) UDP port, for the rare game that does not follow the `game port + 1` convention (`0` = convention). Games whose port would be 65535 must set it explicitly |
| `stop_command` | string | — | `quit` | Console command sent for a graceful stop (typed into the process stdin) |
| `platform` | string | — | host default | `linux` or `windows`. Forces the depot SteamCMD downloads (`+@sSteamCmdForcePlatformType`) and the platform-specific environment inside containers |
| `windows_only` | bool | — | `false` | Marks a game without a Linux build. Requires `platform: windows`; such servers must use the Docker backend |
| `docker_image` | string | — | Wine/Steam image default | Container image used when this server runs on the Docker backend. Windows-only games point it at the Wine image |
| `env` | map | — | — | Environment seeded into the server's `env.json` (see [env](#env)) |
| `workshop` | bool | — | `false` | Declares that the game uses Steam Workshop content. Currently informational |
| `config_files` | list | — | — | Server-relative paths shown in **Server → Config** once the file exists (simple form) |
| `config_editors` | list | — | — | Richer form of `config_files`: path + label + description + editor (see [config editors](#config-files-and-config-editors)) |
| `launch_options` | list | — | — | Typed fields collected at creation and appended to the command line (see [launch options](#launch-options)) |
| `expose` | list | — | game port (tcp) | Host ports a containerized server publishes (see [expose](#expose)) |
| `save_dirs` | list | — | — | Directories with world/save data. Validated, currently descriptive metadata |
| `backup_dirs` | list | — | — | Folders (single files work too, e.g. `serverconfig.xml`) a backup of this server should cover, relative to the server directory. Frozen into each new server and pre-selected in **Server → Backups**; changing the manifest never changes servers that already exist. Empty = back up the whole server directory |
| `notes` | string | — | — | Free text shown to administrators when picking the game |

### Launch command placeholders

| Placeholder | Value |
| --- | --- |
| `{SERVER_NAME}` | The server name typed in the wizard (or applied from a recipe) |
| `{PORT}` | The chosen game port, or the manifest's `port` when none was chosen |
| `{QUERY_PORT}` | `query_port`, or `port + 1` |
| `{SAVE_DIR}` | The server's `saves` directory (`<data>/servers/<id>/saves`). Inside a container the server directory is mounted at the work dir, so the path is rewritten for the container |

Placeholders are replaced **inside already-parsed arguments**; the resulting
command is executed directly (`execve`), never through a shell. Typed option
values therefore cannot inject shell syntax. If a game genuinely needs shell
logic, wrap it in a script inside the server directory and point `run_command`
at that script.

### Launch options

`launch_options` turns wizard inputs into argv elements:

```yaml
launch_options:
  - key: server_name        # unique id used by the API/recipes
    label: Server name
    description: Shown in the server browser.
    type: text
    source: server_name     # filled in by Servy, not asked again
    flag: -name
  - key: port
    label: Game port
    type: number
    source: port
    flag: -port
    min: 1
    max: 65535
  - key: public
    label: Public listing
    type: boolean
    flag: -public
    default: "true"
  - key: password
    label: Server password
    type: password
    flag: -password
    required: true
    min_length: 5
    max_length: 64
  - key: difficulty
    label: Difficulty
    type: select
    flag: -difficulty
    options:
      - { value: easy,   label: Easy }
      - { value: normal, label: Normal }
    default: normal
```

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `key` | string | ✅ | Unique option id: lowercase letter first, then lowercase letters/digits/underscores, ≤ 64 chars |
| `label` | string | ✅ | Field label in the UI (≤ 100 chars) |
| `type` | string | ✅ | `text`, `password`, `number`, `boolean` or `select` |
| `description` | string | — | Helper text (≤ 500 chars) |
| `flag` | string | — | Single argv token emitted before the value (e.g. `-name`). No whitespace/control characters, ≤ 128 chars. Without a flag the value is emitted on its own |
| `flag_separator` | string | — | Joins flag and value into one argv element (e.g. `=` → `-PORT=7777`). Needs `flag`, ≤ 16 chars, no whitespace |
| `source` | string | — | Value Servy fills in itself: `server_name` (type `text`), `port` / `query_port` (type `number`), `save_dir` (type `text`). Cannot be combined with `default`, must not be submitted by the client |
| `default` | string | — | Value used when the user leaves the field empty. Validated like a user value |
| `required` | bool | — | Creation fails with a clear error when no value is available |
| `min` / `max` | int | — | Bounds for `number` options |
| `min_length` / `max_length` | int | — | Length bounds for `text` / `password` options (`0 … 4096`) |
| `true_value` / `false_value` | string | — | For `boolean` options: value emitted instead of the flag (e.g. `true_value: 1`). Single tokens, ≤ 256 chars |
| `options` | list | ✅ for `select` | 1–64 choices, each with a unique `value` (≤ 256 chars, single token) and a `label` (≤ 100 chars) |

How values become arguments:

| Situation | Arguments emitted |
| --- | --- |
| text/number/password/select with `flag`, no separator | `-flag` `value` |
| … with `flag_separator: "="` | `-flag=value` |
| no `flag` | `value` |
| boolean with `flag`, no mappings, `true` | `-flag` |
| boolean with `flag`, no mappings, `false` | *(nothing)* |
| boolean with `true_value`/`false_value` | `-flag` `mapped-value` (or `-flag=mapped` with a separator); with no flag: the mapped value itself |
| option not supplied, no `default`, not `required` | *(nothing)* |
| `source` option | The value Servy knows (server name, port, query port, save dir), then the same rules |

Validation rules (enforced both by the UI and by the server):

- at most **64** options per manifest, keys unique;
- every value is a single token without NUL/line breaks/control characters,
  ≤ 4096 characters;
- `min`/`max` exist only for `number`; `min_length`/`max_length` only for
  `text`/`password`; `true_value`/`false_value` only for `boolean`;
- `default` is validated against the option's own rules at parse time;
- option values are typed fields, **not** shell input — a value like
  `$(reboot)` becomes literal argv text.

**Secrets:** `password` values are stored in the panel database together with
that server's snapshot. The server list/detail view never includes them, the
settings UI masks them (revealable by anyone who may edit the server), and at
runtime they are passed to the game as argv elements — visible in `ps` to
operating-system administrators. Servy does not encrypt the database for this
feature, so treat database backups as sensitive.

### Expose

`expose` declares which host ports a Docker-backed server publishes. An empty
list falls back to the legacy behaviour (game port over TCP, plus UDP for
Bedrock):

```yaml
expose:
  - proto: udp                       # default tcp
  - offset: 1                        # game port + 1 (query port)
    proto: udp
    optional: true                   # may stay closed without blocking a start
  - port: 27020                      # fixed in-container port
    proto: tcp
```

| Field | Type | Meaning |
| --- | --- | --- |
| `port` | int | Fixed in-container port (`1 … 65535`). Wins over `offset` |
| `offset` | int | Added to the server's game port when `port` is absent (`0 … 65535`) |
| `proto` | string | `tcp` (default) or `udp` |
| `optional` | bool | Hint that the port may be absent; a conflict on an optional port never blocks a start |

The resolved port must be a valid port number. Publishing happens through the
Docker engine; the host firewall, the cloud security group and (for the panel
container) the compose port mappings still have to allow it.

### Config files and config editors

```yaml
# simple form: paths listed in the Config tab once they exist
config_files:
  - server.cfg

# rich form
config_editors:
  - path: saves/adminlist.txt
    label: Admin list
    description: One player ID per line.
    editor: lines
```

| Field | Type | Meaning |
| --- | --- | --- |
| `path` | string | Server-relative file path. Must stay inside the server directory, no drive prefixes, no NUL, unique after normalisation |
| `label` | string | Display name (≤ 100 chars) |
| `description` | string | Helper text (≤ 500 chars) |
| `editor` | string | `auto` (by extension, default), `raw` (plain text area) or `lines` (one entry per row — handy for allow/ban/admin lists) |

Both lists are capped at **128** entries and only *describe* files: Servy never
creates them. Start the game once, then refresh the Config tab. All other files
in the server directory stay editable through the normal extension-based editor.

### Env

```yaml
env:
  LD_LIBRARY_PATH:
    - ./linux64
  DOTNET_ROOT:
    - ./dotnet
    - mode:append
```

`env` seeds the server's `env.json`. The simple form is a list of values merged
with the default mode; the two-element form sets the merge mode explicitly
(`mode:append`, `mode:set`, …). Variable names must be non-empty and free of
`=`, NUL and line breaks. Env values are written literally — no shell
expansion.

### A complete example (Valheim-style)

```yaml
app_id: 896660
slug: valheim
name: Valheim
executable: valheim_server.x86_64
# only the four supported placeholders go into the template; everything the
# launch_options contribute (-name, -port, -public, -world, -password) is
# appended to these argv elements
run_command: ./valheim_server.x86_64 -savedir {SAVE_DIR}
stop_command: stop
port: 2456
port_name: game
env:
  LD_LIBRARY_PATH: [./linux64, "mode:append"]

launch_options:
  - key: server_name
    label: Server name
    type: text
    source: server_name
    flag: -name
    max_length: 64
  - key: port
    label: Game port
    description: UDP game port; the next UDP port is used for Steam queries.
    type: number
    source: port
    flag: -port
    min: 1
    max: 65534
  - key: public
    label: Public listing
    type: boolean
    flag: -public
    default: "true"
    true_value: "1"
    false_value: "0"
  - key: world
    label: World name
    type: text
    flag: -world
    default: Dedicated
    min_length: 1
    max_length: 64
  - key: password
    label: Server password
    type: password
    flag: -password
    required: true
    min_length: 5
    max_length: 64

expose:
  - proto: udp
  - offset: 1
    proto: udp

config_editors:
  - path: saves/adminlist.txt
    label: Admin list
    description: One player ID per line.
    editor: lines

save_dirs: [saves, saves/worlds]
backup_dirs: [saves]
notes: Valheim requires a server password of at least 5 characters.
```

This is the real built-in `valheim` manifest, trimmed. The launch command ends
up as:

```
./valheim_server.x86_64 -savedir /data/servers/<id>/saves \
  -name "My server" -port 2456 -public 1 -world Dedicated -password hunter2
```

— every token passed as its own argv element, quotes included only where the
template itself had them.

## Managing the catalog

**Panel:** *Game manifests* → paste YAML/JSON → **Validate** → **Save**.
Changes are live immediately, no restart.

**CLI** (run against the same data directory the panel uses):

```bash
servy --data /srv/servy game-manifest validate --file ./my-game.yaml
servy --data /srv/servy game-manifest add      --file ./my-game.yaml   # add or replace
servy --data /srv/servy game-manifest list
servy --data /srv/servy game-manifest remove   --slug my-game          # shows the built-in again
```

The CLI writes the overlay atomically, but a running panel does not watch
external file edits — restart it after CLI changes. Catalog edits are **not**
replicated to cluster nodes: configure the catalog on each agent that should
offer the same games.

**API:** `GET /api/v1/panel/steam/catalog` (list),
`POST /api/v1/panel/steam/catalog/custom/validate`,
`POST /api/v1/panel/steam/catalog/custom` (`{"manifest":"<YAML/JSON text>"}`),
`DELETE /api/v1/panel/steam/catalog/custom/{slug}` — all superuser-only except
the list.

**Trust warning.** `run_command`, `stop_command`, `docker_image`, `env` and
`config_editors` are administrator-controlled executable configuration: a saved
manifest runs whatever it says inside your servers. Only import manifests from
sources you trust.

---

# Server-creation recipes

A recipe is a stored JSON document describing *how to build one server*. Users
with the `SERVER_CREATION` panel permission can apply it without knowing the
details — the panel stages files, downloads and installs for them.

Recipes are JSON only, at most **4 MiB**, exactly one object, unknown fields
rejected. Importing a recipe with the same `id` replaces the stored one.

## Minimal recipes per type

```json
{ "id": "paper", "name": "Paper (1.21)", "create_type": "download_jar",
  "type": "minecraft-java", "jar_url": "https://api.papermc.io/…/paper.jar",
  "min_mem": "2G", "max_mem": "4G", "port": 25565 }
```

```json
{ "id": "modpack", "name": "Modpack (ZIP)", "create_type": "import_zip",
  "type": "minecraft-java", "archive_url": "https://downloads.example.org/pack.zip" }
```

```json
{ "id": "valheim-friends", "name": "Valheim (friends)", "create_type": "steam_game",
  "type": "steam_cmd", "steam_slug": "valheim", "port": 2456,
  "steam_options": { "public": "false", "world": "Midgard" } }
```

```json
{ "id": "my-raw", "name": "My Server", "create_type": "raw_exec",
  "type": "custom", "executable": "server", "execution_command": "./server --port 27015",
  "port": 27015, "stop_command": "stop" }
```

## Complete example

```json
{
  "id": "example-zip-server",
  "name": "Example ZIP Server",
  "version": "1",
  "type": "custom",
  "create_type": "import_zip",
  "archive_url": "https://downloads.example.org/server.zip",
  "port": 27015,
  "min_mem": "1G",
  "max_mem": "2G",
  "stop_command": "stop",
  "execution_backend": "docker",
  "docker_image": "ghcr.io/example/server:latest",
  "docker_env": { "GAME_MODE": "survival" },
  "docker_ports": ["27015:27015/udp"],
  "docker_binds": ["/srv/data:/data:ro"],
  "docker_network": "bridge",
  "docker_workdir": "/data",
  "docker_autoremove": true,
  "java_selection": "17",
  "shell": "bash",
  "auto_start": true,
  "crash_detection": true,
  "files": {
    "config/server.properties": "motd=Example\nmax-players=20",
    "config/ops.json": "https://downloads.example.org/ops.json"
  },
  "backup_dirs": ["world", "plugins", "server.properties"],
  "config": { "motd": "Example server", "max-players": "20" },
  "env": { "GAME_MODE": "survival" }
}
```

## Field reference

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `name` | string | ✅ | Display name; also the default server name when applying (`≤ 120` chars) |
| `id` | string | — | Stable recipe id: lowercase letters, digits, hyphens, `1–64` chars, must start/end with a letter or digit. Derived from `name` when omitted; importing the same id replaces the recipe |
| `version` | string | — | Free-form version label, default `"1"` (informational) |
| `type` | string | — | Server type: `minecraft-java` (default), `minecraft-bedrock`, `steam_cmd`, `custom`, `hytale`. Inferred from `create_type` when omitted (`steam_game` → `steam_cmd`, `raw_exec` → `custom`) |
| `create_type` | string | — | How the server is provisioned: `download_jar`, `import_zip`, `steam_game`, `raw_exec`. **Inferred** from whichever URL/slug/command field is present; an error is raised when nothing matches |
| `jar_url` | string | for `download_jar` | HTTPS/HTTP URL of a runnable JAR |
| `jar_name` | string | — | File name to store the JAR as (default: last path segment of `jar_url`) |
| `archive_url` | string | for `import_zip` | HTTPS/HTTP URL of a ZIP archive |
| `steam_slug` | string | one of these for `steam_game` | Slug of the SteamCMD game manifest to install |
| `app_id` | string | one of these for `steam_game` | Steam app ID as a string; must be a positive 32-bit number. Unknown IDs fall back to a generic `./run.sh` server |
| `steam_options` | map | — | Values for the game's `launch_options` (key → string). Only valid for `steam_game`; at most 64 entries, keys must match the manifest's option keys |
| `port` | int | — | Game port. `0`/omitted = the type's default (25565 Java, 19132 Bedrock, 27015 Steam, 25565 custom) |
| `min_mem` / `max_mem` | string | — | `-Xms` / `-Xmx` values (e.g. `512M`, `4G`) for JAR-based servers; defaults `128M` / `256M` |
| `stop_command` | string | — | Console command for a graceful stop (default `quit`) |
| `executable` | string | for `raw_exec` | File to start |
| `execution_command` | string | for `raw_exec` | Command line, executed through the recipe's `shell` (default `auto`). Unlike Steam games, recipes get no automatic placeholder substitution — write the literal arguments |
| `execution_backend` | string | — | `process` (host process) or `docker` (sibling container). Empty = process |
| `docker_image` | string | with docker backend | Container image |
| `docker_env` | map | — | Environment for the container. `SERVY_DOCKER_USER` is special: it selects the container user (`image` = the image's default) and is not passed to the container |
| `docker_ports` | list | — | Extra port mappings, e.g. `["27015:27015/udp"]` |
| `docker_binds` | list | — | Extra volumes, e.g. `["/srv/data:/data:ro"]` |
| `docker_network` | string | — | Docker network name |
| `docker_workdir` | string | — | Working directory inside the container |
| `docker_autoremove` | bool | — | Remove the container when it exits |
| `java_selection` | string | — | Java version to use, e.g. `"17"` (see `GET /panel/java` for detected installs) |
| `shell` | string | — | Terminal/exec shell for `raw_exec` servers (`bash`, `sh`, `cmd`, …) |
| `auto_start` | bool | — | Start the server when the panel starts; also starts it right after creation unless files/config still have to be written first |
| `crash_detection` | bool | — | Watch the process and report a crash |
| `backup_dirs` | list | — | Folders (single files work too) a backup of servers created from this recipe covers, relative to the server directory, e.g. `["world", "plugins", "server.properties"]`. Stored on the server at creation and pre-selected in **Server → Backups**, where it can be changed afterwards. Empty = back up the whole server directory |
| `files` | map | — | Files to write after creation: path → literal text or HTTP(S) URL |
| `config` | map | — | Values merged into `server.properties` |
| `env` | map | — | Seed for the server's `env.json` (values may be lists/objects) |

## `create_type` semantics

| `create_type` | Needs | What Servy does |
| --- | --- | --- |
| `download_jar` | `jar_url` | Downloads the JAR (max 2 GiB, 15 min) into the server directory and builds a Java command from `min_mem` / `max_mem` |
| `import_zip` | `archive_url` | Downloads the ZIP (max 2 GiB), extracts it, detects the executable, stores it as the update source |
| `steam_game` | `steam_slug` or `app_id` | Installs the game with SteamCMD using the referenced game manifest and freezes it into the server |
| `raw_exec` | `executable` + `execution_command` | Creates the directory and stores the command as given |

`steam_options` are also possible only for `steam_game` and are validated
against the referenced game manifest when the server is created — unknown keys
fail the creation instead of being passed to the game.

## Files, config and env

- `files` — at most **128** entries. Paths must be server-relative, may not
  escape the server directory (no `..`, no absolute paths, no `C:` prefixes, no
  NUL) and must be unique after normalisation. A value starting with `http://`
  or `https://` is **downloaded** (max **4 MiB** per file, **16 MiB** in total,
  30 s timeout); anything else is written literally. Directories are created as
  needed.
- `config` — key/value pairs written into `server.properties` (keys without
  `=`, `:`, line breaks or empty names; values without line breaks). This runs
  *after* the server is created, so the game is started once the file exists.
- `env` — serialised into `env.json` and merged with the environment the server
  already has.

## Backup folders

`backup_dirs` tells Servy what a backup of the created server should contain —
the same idea as `backup_dirs` in a SteamCMD game manifest, for recipes:

```json
{ "backup_dirs": ["world", "plugins", "server.properties"] }
```

- Entries are relative to the server directory. A folder means "this folder and
  everything inside it"; a file name means "just that file".
- The list is copied onto the server when it is created, so editing the recipe
  later does not silently change what an existing server backs up.
- It only pre-selects the checkboxes in **Server → Backups → New/Edit backup
  config**; nothing is backed up until a backup config exists (`auto_start`
  servers get a default config automatically — that one uses the same list).
- Leave it out (or empty) and backups cover the whole server directory, which is
  what every server created before this option existed keeps doing.

## Safety and limits

- URLs are fetched by the panel host, reject loopback/private/link-local/reserved
  destinations (including after redirects), and are size- and time-limited.
- ZIP extraction rejects unsafe paths and symlinks, caps archives at **100,000
  entries** and **8 GiB expanded**, and removes a partially extracted server on
  failure.
- If staging files or writing config fails after the server row was created,
  Servy rolls the creation back (deletes the server) so no half-configured
  server stays behind.
- Importing recipes is a superuser action; **applying** one only needs
  `SERVER_CREATION`. Everything a recipe downloads executes on your host —
  treat public recipe URLs like installers from the internet.

## Importing and applying

**CLI**

```bash
servy --data /srv/servy manifest add --file ./paper-recipe.json
servy --data /srv/servy manifest add --url https://manifests.example.org/paper.json
servy --data /srv/servy manifest list
```

**Panel / API**

| Action | Call |
| --- | --- |
| List recipes | `GET /api/v1/panel/manifests` |
| Import from JSON | `POST /api/v1/panel/manifests` `{"manifest":"<JSON text>"}` (superuser) |
| Import from URL | `POST /api/v1/panel/manifests` `{"url":"https://…"}` (superuser) |
| Read one | `GET /api/v1/panel/manifests/{definition_id}` |
| Delete | `DELETE /api/v1/panel/manifests/{definition_id}` (superuser) |
| Apply | `POST /api/v1/panel/manifests/apply` `{"definition_id":"…","name":"optional override"}` or `{"url":"https://…","name":"…"}` |

Applying requires the `SERVER_CREATION` permission and respects the user's
server limit. The created server belongs to the applying user; an optional
`name` in the apply call overrides the recipe's name.
