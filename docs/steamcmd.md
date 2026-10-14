# SteamCMD games

Servy installs and runs Steam dedicated servers (`app_id` based) the same way it
runs Minecraft servers: pick a game in the wizard, the panel downloads the depot
with SteamCMD, stores the launch command together with the values you entered,
and from then on start/stop/console/files/backups work like any other server.

The game list itself is a manifest — one file per game, describing the Steam app
ID, the launch command, ports, config files and typed setup fields. Servy ships
40 curated games and lets you add your own. The full manifest format is in
[`manifests.md`](manifests.md#steamcmd-game-manifests).

## Creating a Steam server

1. **Servers → Create server**, pick **SteamCMD** as the type.
2. Choose a game from the catalog. The step shows the game's default port and
   its typed fields (server name, password, map, player limit, … depending on
   the game). Fields marked with a `*` are required; the server name and ports
   are filled in automatically.
3. Pick the node, the port and the remaining options (**auto-start**,
   **crash detection**), and create.
4. Installation output streams live into the creation dialog. When it finishes,
   the server appears in the list with its game-specific defaults and can be
   started.

The same flow through the API:

```bash
curl -X POST http://localhost:8443/api/v1/servers \
  -H 'Authorization: Bearer <token>' -H 'content-type: application/json' \
  -d '{
        "create_type": "steam_game",
        "type": "steam_cmd",
        "name": "Valheim — friends",
        "steam_slug": "valheim",
        "port": 2456,
        "steam_options": { "public": "true", "world": "Midgard" },
        "auto_start": true,
        "crash_detection": true
      }'
```

`steam_slug` is the catalog slug; `app_id` works as an alternative. When the app
ID is not in the catalog Servy falls back to a generic server that runs
`./run.sh`, so unknown games are better added as a game manifest first
(`servy game-manifest add`, see [Custom games](#custom-games)).
`steam_options` keys are the `key` values of the game's `launch_options`;
unknown keys are rejected before anything is downloaded.

Query ports follow the Source convention `game port + 1` unless the catalog
declares an explicit `query_port`. The wizard validates the combination, so a
game that would need port 65536 fails before anything is downloaded.

### Where ports come from

| Execution backend | Ports |
| --- | --- |
| **process** (default) | The server binds the configured game port directly; nothing to publish |
| **docker** | Servy publishes the ports declared by the game's `expose` list (game port, query port, extra ports) and stops a start with `PORT_CONFLICT` when a port is already taken |

Either way the ports must be open in the host firewall / cloud security group
and — for the panel container — mapped in `docker-compose.yml`.

## Where SteamCMD comes from

Servy picks the first SteamCMD it can use:

| Situation | Used |
| --- | --- |
| Panel runs in the official container (SteamCMD pre-installed in `/opt/steamcmd`) | That copy |
| A managed copy exists in `<data>/steamcmd`, or a system install at `/opt/steamcmd/steamcmd.sh` / `/usr/games/steamcmd` | That binary |
| Otherwise, a usable SteamCMD does not exist | Servy downloads it into `<data>/steamcmd` on first use |
| Docker execution backend available | `steamcmd/steamcmd:latest` container (`docker_steam_image` in settings) is preferred on hosts, and used as a fallback when a local run fails |

Installing a Steam server requires either:

- **Docker** (recommended): nothing to install, the official SteamCMD image is
  pulled and the install directory is bind-mounted into it; or
- a **host SteamCMD**: the panel container's copy, a distro package
  (`steamcmd` in Debian/Ubuntu), or the managed download in `<data>/steamcmd`.

Windows-only depots are fetched with
`+@sSteamCmdForcePlatformType windows` (and the Docker equivalent
`SteamCmdForcePlatformType=windows`), so the Linux-hosted panel receives the
correct depot.

## Windows-only games

Some titles have no Linux dedicated server. The catalog marks them
`windows_only: true` with `platform: windows` and a `docker_image` pointing at
the Wine image:

Abiotic Factor, ARK: Survival Ascended, Astroneer, Conan Exiles, Enshrouded,
Icarus, Sons of the Forest, The Forest and V Rising (all marked *Windows/Wine*
in the [catalog table](#built-in-catalog) below).

These servers **must** use the Docker execution backend, and the panel needs the
`servy-wine` image:

```bash
make docker-wine
docker tag servy-wine:latest ghcr.io/flameinthedark/servy-wine:latest
```

See [`docker.md`](docker.md#windows-only-game-servers-wine-image) and
[`docker/steamcmd-wine/README.md`](../docker/steamcmd-wine/README.md).

## Built-in catalog

| Slug | Game | App ID | Default port |
| --- | --- | --- | --- |
| `7dtd` | 7 Days to Die | 294420 | 26900 |
| `abiotic-factor` | Abiotic Factor (Windows/Wine) | 2857200 | 7777 |
| `ark-asa` | ARK: Survival Ascended (Windows/Wine) | 2430930 | 7777 |
| `ark-se` | ARK: Survival Evolved | 376030 | 7777 |
| `arma-reforger` | Arma Reforger | 1874900 | 2001 |
| `arma3` | Arma 3 | 233780 | 2302 |
| `astroneer` | Astroneer (Windows/Wine) | 728470 | 8777 |
| `avorion` | Avorion | 565060 | 27000 |
| `barotrauma` | Barotrauma | 1026340 | 27015 |
| `black-mesa` | Black Mesa | 346680 | 27015 |
| `conan-exiles` | Conan Exiles (Windows/Wine) | 443030 | 7777 |
| `cs-source` | Counter-Strike: Source | 232330 | 27015 |
| `cs2` | Counter-Strike 2 | 730 | 27015 |
| `dod-source` | Day of Defeat: Source | 232290 | 27015 |
| `eco` | Eco | 739590 | 3001 |
| `enshrouded` | Enshrouded (Windows/Wine) | 2278520 | 15636 |
| `fistful-of-frags` | Fistful of Frags | 295230 | 27015 |
| `garrys-mod` | Garry's Mod | 4020 | 27015 |
| `hl2dm` | Half-Life 2: Deathmatch | 232370 | 27015 |
| `icarus` | Icarus (Windows/Wine) | 2089300 | 17777 |
| `insurgency-sandstorm` | Insurgency: Sandstorm | 581330 | 27102 |
| `killing-floor-2` | Killing Floor 2 | 232130 | 7777 |
| `l4d` | Left 4 Dead | 222840 | 27015 |
| `l4d2` | Left 4 Dead 2 | 222860 | 27015 |
| `mordhau` | Mordhau | 629800 | 7777 |
| `necesse` | Necesse | 1169370 | 14159 |
| `nmrih` | No More Room in Hell | 317670 | 27015 |
| `palworld` | Palworld | 2394010 | 8211 |
| `project-zomboid` | Project Zomboid | 380870 | 16261 |
| `rust` | Rust | 258550 | 28015 |
| `satisfactory` | Satisfactory | 1690800 | 7777 |
| `sons-of-the-forest` | Sons of the Forest (Windows/Wine) | 2465200 | 8766 |
| `soulmask` | Soulmask | 3017310 | 8777 |
| `squad` | Squad | 403240 | 7787 |
| `terraria` | Terraria (TShock) | 105600 | 7777 |
| `tf2` | Team Fortress 2 | 232250 | 27015 |
| `the-forest` | The Forest (Windows/Wine) | 556450 | 8766 |
| `unturned` | Unturned | 1110390 | 27015 |
| `v-rising` | V Rising (Windows/Wine) | 1829350 | 9876 |
| `valheim` | Valheim | 896660 | 2456 |

```bash
servy --data /srv/servy game-manifest list          # same list from the CLI
curl -s -H 'Authorization: Bearer <token>' \
  http://localhost:8443/api/v1/panel/steam/catalog | jq '.data.games[].slug'
```

## Custom games

Anything not in the table above can be added with a manifest — either the
**Game manifests** page in the panel or the CLI:

```bash
servy --data /srv/servy game-manifest validate --file ./my-game.yaml   # check only
servy --data /srv/servy game-manifest add      --file ./my-game.yaml   # save/override
servy --data /srv/servy game-manifest remove   --slug my-game          # back to built-in
servy --data /srv/servy game-manifest list
```

```yaml
app_id: 999999
slug: my-game
name: My Game
run_command: ./server -name {SERVER_NAME} -port {PORT} -queryport {QUERY_PORT}
executable: server
stop_command: quit
port: 27015
```

Custom entries live in `<data>/config/steamgames.json` and can override a
built-in entry only when the app ID matches; deleting the override restores the
built-in. A running panel reloads catalog changes made through its API
immediately; after a **CLI** edit, restart the panel.

Every field of the game manifest — launch options, placeholders, `expose`,
`config_editors`, `env`, platform flags and validation limits — is documented in
[`manifests.md`](manifests.md#steamcmd-game-manifests).

## Updating an installed Steam server

**Today the panel installs the current depot when the server is created; it has
no Steam update button yet.** The generic
`POST /api/v1/servers/{id}/update_executable` endpoint drives Minecraft/Bedrock
and custom-URL updates and answers `UPDATE_FAILED: steam updates run via
steamcmd app_update` for `steam_cmd` servers.

Until that is wired up, update a Steam server by running SteamCMD against its
directory yourself — the same command the panel uses:

```bash
# inside the panel container (steamcmd is at /opt/steamcmd)
/opt/steamcmd/steamcmd.sh \
  +force_install_dir /data/servers/<server-id> \
  +login anonymous \
  +app_update 896660 validate \
  +quit
```

Stop the server first, and take a backup (Servers → server → Backups). For
Docker-backed servers the equivalent is:

```bash
docker run --rm -it \
  -v "$PWD/data/servers/<server-id>:/data" \
  steamcmd/steamcmd:latest \
  +force_install_dir /data +login anonymous +app_update 896660 validate +quit
```

Launch settings do not need to be touched: the frozen game definition keeps them
and only the files on disk change.

## Troubleshooting

| Symptom | Cause / fix |
| --- | --- |
| `steamcmd not installed and docker unavailable` | Enable the Docker backend, install the `steamcmd` package, or let the panel download SteamCMD (needs outbound access to `steamcdn-a.akamaihd.net`) |
| Install stops at *Waiting for user info…* or `FAILED (Invalid Platform)` | The depot has no build for the requested platform; Windows-only games need `platform: windows` plus the Wine image |
| `PORT_CONFLICT` when starting | Another server (or a stale container) holds a declared port. Optional exposure ports never block a start; required ones do |
| Console shows nothing although the server is up | Give the game a few seconds, then check the log tab; some games only start logging after their first world load |
| Steam install works, server exits immediately | Verify the launch command in the game manifest (`run_command`) against the files in the server directory — a renamed binary or a missing Steam client library is the usual cause. Check the console tab for the game's own error |
| Disk fills up during install | Depots are extracted to `<data>/servers/<id>`; make sure the data disk has room for the final download **and** the extraction (~2× the depot size) |
| Docker-backed server keeps restarting | Look at the container logs (`docker ps -a`, `docker logs`); the Wine image documents its environment in [`docker/steamcmd-wine/README.md`](../docker/steamcmd-wine/README.md) |
