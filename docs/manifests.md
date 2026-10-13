# Manifest workflows

Servy has **two different manifest systems**. They solve different problems and are not interchangeable:

| Manifest | Purpose | Stored in | When it takes effect |
| --- | --- | --- | --- |
| SteamCMD game manifest | Describes a Steam app to install and launch (app ID, command, ports, platform) | `<data>/config/steamgames.json` | Immediately in the Create Server game picker; new servers only |
| Server-creation recipe | A reusable recipe for creating a server (download a JAR, import a ZIP, select a Steam game, or run a custom executable) | Servy's database (`definitions`) | When a user applies the recipe to create a server |

A saved manifest is a **creation template**, not a live patch to an existing server. It does not reinstall or rewrite servers that already exist. New SteamCMD servers keep a snapshot of the game definition and launch settings used at creation, so later catalog edits affect future servers only. To change a deployed server, edit its actual files under **Servers → server → Config/Files**, then restart it as needed.

## SteamCMD game manifests

### Through the panel

1. Sign in as a panel superuser and open **Game manifests** in the sidebar.
2. Start from the example, or paste a YAML/JSON manifest. The form has **Validate** and **Save** actions; validation happens before a definition is written.
3. Select the saved entry in **Create Server → SteamCMD**. The app is installed with SteamCMD, and the manifest's launch command, stop command, default port, platform, and container port declarations are used for that new server.
4. The saved definition is available immediately. No panel restart or image rebuild is needed.

The editor's trust warning is important: `run_command`, `stop_command`, `docker_image`, and environment settings are administrator-controlled executable configuration. Only save definitions you trust. Typed launch-option values can include passwords; Servy stores the values with that new server's definition in the panel database, does not return them in the server API view, and passes them to the game process as separate arguments. The database is not encrypted by this feature, and operating-system administrators can inspect process arguments while a server is running. Do not put real secrets in a public recipe or share server database backups casually.

### Through the CLI

The CLI and panel use the same JSON overlay. Run these commands on the host/container that owns Servy's data directory:

```sh
# Validate without changing the catalog
servy --data /srv/servy game-manifest validate --file ./my-game.yaml

# Persist/replace the custom entry
servy --data /srv/servy game-manifest add --file ./my-game.yaml

# Inspect built-in and custom entries
servy --data /srv/servy game-manifest list

# Remove only a custom entry; a matching built-in returns
servy --data /srv/servy game-manifest remove --slug my-dedicated-server
```

`--data` may be omitted if `SERVY_DATA_DIR` is set. The path in the CLI must be the same data volume used by the running panel. A CLI edit writes the file atomically, but a panel process that is already running does not watch external file edits: restart Servy after a CLI add/remove. Changes made through the panel API update its live catalog immediately. The overlay lives in the panel's data directory and is not replicated to cluster agents; configure the catalog separately on each agent that needs the same games.

### YAML example

```yaml
app_id: 999999
slug: my-dedicated-server
name: My Dedicated Server
run_command: ./server -queryport {QUERY_PORT} -savedir {SAVE_DIR}
executable: server
stop_command: quit
port: 27015
port_name: game

# Servy supplies the server name and selected game port to these options.
# Other options are typed fields in Create Server.
launch_options:
  - key: server_name
    label: Server name
    type: text
    source: server_name
    flag: -name
  - key: port
    label: Game port
    type: number
    source: port
    flag: -port
    min: 1
    max: 65535
  - key: world
    label: World name
    type: text
    flag: -world
    default: Dedicated
  - key: password
    label: Server password
    type: password
    flag: -password
    required: true
    min_length: 5
    max_length: 64

# Ports published when this server uses the Docker execution backend.
# With no explicit port, game port + offset is used.
expose:
  - proto: udp
  - offset: 1
    proto: udp
    optional: true

# Optional SteamCMD platform override. Required for Windows-only depots.
# platform: windows
# windows_only: true
# docker_image: ghcr.io/example/game-wine:latest

# env.json seed. The two-element form can specify the env merge mode.
env:
  LD_LIBRARY_PATH:
    - ./linux64
    - mode:append

# Legacy/simple declarations; the config editor lists each file once it exists.
config_files:
  - server.cfg
# Rich descriptors can label files, explain their format, and select a line editor.
config_editors:
  - path: saves/adminlist.txt
    label: Admin list
    description: One player ID per line.
    editor: lines
save_dirs:
  - saves
backup_dirs:
  - saves
notes: "Any notes for the administrator"
```

Required fields are `app_id` (positive Steam application ID), `slug`, `name`, `run_command`, `executable`, and `port`. The parser rejects unknown keys and manifests larger than 1 MiB. JSON is accepted too; the JSON field names match the YAML names.

Available launch-command placeholders are:

- `{SERVER_NAME}` — the server name entered in the creation wizard.
- `{PORT}` — the selected game port (or the manifest default).
- `{QUERY_PORT}` — explicit `query_port`, or the game port plus one by default.
- `{SAVE_DIR}` — the server's persistent `saves` directory (translated for Docker execution).

`launch_options` adds typed fields to **Create Server** and safely appends each flag/value as its own argument. Each entry needs a unique `key`, a user-facing `label`, and a `type`: `text`, `password`, `number`, `boolean`, or `select`. Optional `flag` is a single command-line token. `default`, `required`, numeric `min`/`max`, and text/password `min_length`/`max_length` are validated by both the UI and server. A `select` also needs `options: [{value, label}]`. Boolean values use `true_value`/`false_value` if supplied; with a flag and no mappings, `true` emits the flag and `false` omits it.

Use `source` when Servy already collects a value in the wizard: `server_name`, `port`, `query_port`, or `save_dir`. The source value is validated like any other option, but users do not re-enter it. `run_command` is treated as an argv-style command template: placeholder substitution happens within parsed arguments, and typed options are appended without concatenating user input into a shell command. Existing shell operators are not a substitute for a dedicated wrapper script.

`config_files` is the backward-compatible list of server-relative file paths. `config_editors` adds `{path, label, description, editor}` metadata without changing old manifests; `editor: lines` opens a row-based one-entry-per-line editor (useful for admin, allow, and ban lists). These files appear under **Server → Config** only after they exist, including nested declared paths. Servy does not create missing game files; start the game once, then refresh the Config tab. Other files keep their extension-based editor.

`expose` declares Docker-published ports. Each item may use `port` (fixed in-container port) or `offset` (added to the selected game port), `proto: tcp|udp`, and `optional: true` for a port that may not be opened by every mode. If `expose` is empty, Servy retains its legacy game-port/TCP behavior (Bedrock also gets UDP). Docker publishes the declared ports; host firewall, cloud security-group, and router rules are still your responsibility.

`platform` accepts `linux` or `windows`. A `windows_only: true` game must set `platform: windows`; it is started through Docker/Wine. `docker_image` can select a specific runtime image. `env` seeds the server's `env.json` environment settings.

**Metadata caveat:** `config_files` and `config_editors` are now used by the Config tab to label and edit declared files that exist; they do not create files. `save_dirs`, `backup_dirs`, and `workshop` remain descriptive metadata and are not wired to backup selection or Workshop downloads. The default backup behavior is unchanged.

The built-in Valheim manifest demonstrates this setup: the server name and port are bound to the wizard fields; world and password are typed launch options; and `adminlist.txt`, `permittedlist.txt`, and `bannedlist.txt` are declared under the manifest's `-savedir`. The password minimum of five characters follows the [community Valheim server image documentation](https://github.com/community-valheim-tools/valheim-server-docker#basic-docker-usage). Valheim creates the list files on first start, so the panel shows them after a Config-tab refresh. Enter IDs in the format the running Valheim version reports (recent versions may show a `V_` prefix).

### Built-in and custom entries

Built-in definitions live under `internal/steamcmd/games/` and ship with the binary. Custom definitions are a separate overlay in `<data>/config/steamgames.json`. A custom entry can override a built-in entry with the same slug only when its Steam app ID matches; removing that custom override restores the built-in definition. Slugs and Steam app IDs must be unique across the active catalog, except for that same-slug/same-app override case.

## Server-creation recipe manifests

Use a recipe when you want a reusable server-creation choice rather than a new SteamCMD catalog game. Recipes are JSON and can be added from the panel's authenticated API or CLI. Panel import requires a superuser; applying a stored recipe requires the normal server-creation permission.

### CLI import and validation

```sh
# Validate and store a local JSON recipe
servy --data /srv/servy manifest add --file ./paper-recipe.json

# Or fetch, validate, and store a hosted recipe
servy --data /srv/servy manifest add --url https://manifests.example.org/paper.json

servy --data /srv/servy manifest list
```

A local import is parsed and validated before it is stored. The file is stored in the database; it is not copied into `<data>/config/steamgames.json`.

### Recipe example

```json
{
  "id": "example-zip-server",
  "name": "Example ZIP Server",
  "version": "1",
  "type": "custom",
  "create_type": "import_zip",
  "archive_url": "https://downloads.example.org/server.zip",
  "port": 27015,
  "execution_backend": "docker",
  "docker_image": "ghcr.io/example/server:latest",
  "docker_ports": ["27015:27015/udp"],
  "files": {
    "config/server.properties": "motd=Example\nmax-players=20"
  },
  "config": {
    "motd": "Example server",
    "max-players": "20"
  },
  "env": {
    "GAME_MODE": "survival"
  }
}
```

Supported `create_type` values are `download_jar` (`jar_url`), `import_zip` (`archive_url`), `steam_game` (`steam_slug` or `app_id`), and `raw_exec` (`executable` plus `execution_command`). `steam_options` is an optional map of launch-option keys to string values for the selected game's catalog manifest; Servy validates it against that manifest before installing. `app_id` must be a positive 32-bit Steam app ID. `files` maps safe, server-relative paths to inline text or HTTP(S) URLs. `config` seeds `server.properties`; `env` seeds `env.json`. The importer validates file paths and URLs, caps each downloaded file at 4 MiB and all staged files at 16 MiB, and stages downloads before creating the server. A remotely fetched ZIP is capped at 2 GiB compressed; extraction rejects unsafe paths/symlinks, caps archives at 100,000 entries and 8 GiB expanded, and removes the partial server if extraction fails. If applying staged files fails after creation, Servy attempts to delete the partially created server.

External URLs are fetched by the Servy host. Downloads have time/size limits and reject loopback, private, link-local, reserved, and other non-public IP destinations—including unsafe redirect targets. Prefer HTTPS and only import recipes from a trusted publisher; an imported archive or executable can contain arbitrary server code. Do not expose import endpoints to untrusted users.

## Panel API endpoints

All paths below are under `/api/v1` and require the documented authentication/permissions.

### SteamCMD game catalog

- `GET /panel/steam/catalog` — list built-in and custom games.
- `GET /panel/steam/catalog/custom` — list custom overlay entries.
- `POST /panel/steam/catalog/custom/validate` — validate a manifest without saving.
- `POST /panel/steam/catalog/custom` — add/update a custom manifest.
- `DELETE /panel/steam/catalog/custom/{slug}` — remove a custom entry.

The POST body is `{"manifest":"<YAML or JSON text>"}`. These catalog-management routes require a superuser.

### Server-creation recipes

- `GET /panel/manifests` — list stored recipes.
- `POST /panel/manifests` — store a recipe from `{"manifest":"<JSON text>"}` or `{"url":"https://…"}` (superuser).
- `GET /panel/manifests/{definition_id}` — retrieve a stored recipe.
- `DELETE /panel/manifests/{definition_id}` — remove a stored recipe (superuser).
- `POST /panel/manifests/apply` — apply a stored recipe with `{"definition_id":"…","name":"optional override"}` or import/apply a URL with `{"url":"https://…","name":"optional override"}`. Requires server-creation permission and respects the user's server limit.
