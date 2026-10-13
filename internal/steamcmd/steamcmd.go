// Package steamcmd manages the SteamCMD binary and runs installs/updates for
// ANY Steam game server (not just Minecraft), with a curated game catalog
// (name, app id, default run command, config descriptors) + user overlay.
package steamcmd

import (
        "archive/tar"
        "compress/gzip"
        "context"
        "embed"
	"encoding/json"
	"errors"
        "fmt"
        "io"
        "net/http"
        "os"
        "os/exec"
        "path/filepath"
        "runtime"
	"sort"
        "strings"
	"sync"
        "time"

	"github.com/FlameInTheDark/servy/internal/defaults"
	"github.com/FlameInTheDark/servy/internal/dockerx"
)

//go:embed games/*.yaml
var gameConfigs embed.FS

// DefaultWineImage is the container image used to run Steam game servers in
// sibling containers — Windows-only titles via Wine, and native Linux servers
// when the docker backend is selected. It resolves to
// defaults.DockerSteamWineImage; see internal/defaults for the single place to
// change it (ghcr.io/flameinthedark/servy-wine:latest by default, override with
// SERVY_STEAM_WINE_IMAGE).
var DefaultWineImage = defaults.DockerSteamWineImage

// Game describes a supported Steam game server.
type Game struct {
        AppID       int                 `json:"app_id" yaml:"app_id"`
        Name        string              `json:"name" yaml:"name"`
        Slug        string              `json:"slug" yaml:"slug"`
        Type        string              `json:"type" yaml:"type"` // always "steam_cmd"
        RunCommand  string              `json:"run_command" yaml:"run_command"`
        Executable  string              `json:"executable" yaml:"executable"`
        StopCommand string              `json:"stop_command" yaml:"stop_command"`
        Port        int                 `json:"port" yaml:"port"`
        PortName    string              `json:"port_name" yaml:"port_name"`
        Env         map[string][]string `json:"env,omitempty" yaml:"env,omitempty"` // env.json seed
        Workshop    bool                `json:"workshop" yaml:"workshop"`
	ConfigFiles   []string             `json:"config_files,omitempty" yaml:"config_files,omitempty"` // legacy paths, auto-detected editor
	ConfigEditors []ConfigFileEditor   `json:"config_editors,omitempty" yaml:"config_editors,omitempty"`
	LaunchOptions []LaunchOption       `json:"launch_options,omitempty" yaml:"launch_options,omitempty"`
        Notes       string              `json:"notes,omitempty" yaml:"notes,omitempty"`
        SaveDirs    []string            `json:"save_dirs,omitempty" yaml:"save_dirs,omitempty"` // world/save directories to persist
        BackupDirs  []string            `json:"backup_dirs,omitempty" yaml:"backup_dirs,omitempty"` // directories to include in backups (relative to server data dir)
        // QueryPort is the Steam query (A2S) UDP port when it is NOT game_port + 1
        // (the Source convention). 0 means "use game port + 1".
        QueryPort int `json:"query_port,omitempty" yaml:"query_port,omitempty"`
        // Expose declares the host ports a containerized server must publish.
        // Empty means "publish the game port over TCP" (legacy behaviour).
        Expose []ExposedPort `json:"expose,omitempty" yaml:"expose,omitempty"`
        // Platform forces the SteamCMD download platform ("windows", "linux").
        // Required for Windows-only servers so SteamCMD fetches the Windows depot.
        Platform string `json:"platform,omitempty" yaml:"platform,omitempty"`
        // DockerImage is the container image used to run this game's server when the
        // docker execution backend is active (e.g. a Wine-capable image for
        // Windows-only titles). Empty = user must supply one.
        DockerImage string `json:"docker_image,omitempty" yaml:"docker_image,omitempty"`
        // WindowsOnly marks servers without a Linux build — they must run via the
        // docker execution backend with a Wine-capable image (e.g. Enshrouded,
        // Sons of the Forest, Neos VR).
        WindowsOnly bool `json:"windows_only,omitempty" yaml:"windows_only,omitempty"`
}

// ConfigFileEditor gives a declared game config file a friendly name and an
// editor hint. The file is shown only after it exists inside the server folder.
type ConfigFileEditor struct {
	Path        string `json:"path" yaml:"path"`
	Label       string `json:"label,omitempty" yaml:"label,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Editor      string `json:"editor,omitempty" yaml:"editor,omitempty"` // auto | raw | lines
}

// LaunchOption describes one typed value collected when a Steam game server is
// created and appended to its command as a separate, safely quoted argument.
type LaunchOption struct {
	Key         string              `json:"key" yaml:"key"`
	Label       string              `json:"label" yaml:"label"`
	Description string              `json:"description,omitempty" yaml:"description,omitempty"`
	Type        string              `json:"type" yaml:"type"` // text | password | number | boolean | select
	Flag        string              `json:"flag,omitempty" yaml:"flag,omitempty"`
	Source      string              `json:"source,omitempty" yaml:"source,omitempty"` // server_name | port | query_port | save_dir
	Default     string              `json:"default,omitempty" yaml:"default,omitempty"`
	Required    bool                `json:"required,omitempty" yaml:"required,omitempty"`
	Min         *int                `json:"min,omitempty" yaml:"min,omitempty"`
	Max         *int                `json:"max,omitempty" yaml:"max,omitempty"`
	MinLength   *int                `json:"min_length,omitempty" yaml:"min_length,omitempty"`
	MaxLength   *int                `json:"max_length,omitempty" yaml:"max_length,omitempty"`
	TrueValue   string              `json:"true_value,omitempty" yaml:"true_value,omitempty"`
	FalseValue  string              `json:"false_value,omitempty" yaml:"false_value,omitempty"`
	Options     []LaunchOptionValue `json:"options,omitempty" yaml:"options,omitempty"`
}

// LaunchOptionValue is one choice for a select launch option.
type LaunchOptionValue struct {
	Value string `json:"value" yaml:"value"`
	Label string `json:"label" yaml:"label"`
}

// LaunchContext supplies values Servy already knows, such as the server name
// and allocated game port, to manifest options declared with `source`.
type LaunchContext struct {
	ServerName string
	Port       int
	QueryPort  int
	SaveDir    string
}

// GameSnapshot freezes the catalog definition used to create one server. This
// is stored in servers.definitions so later catalog edits only affect new servers.
type GameSnapshot struct {
	Game          Game              `json:"game"`
	LaunchOptions map[string]string `json:"launch_options,omitempty"`
}

// ServerDefinitions is the JSON envelope stored in the existing server
// definitions column. The envelope leaves room for other per-server metadata.
type ServerDefinitions struct {
	SteamGame *GameSnapshot `json:"steam_game,omitempty"`
}

// ExposedPort is one host port a containerized server publishes.
type ExposedPort struct {
        // Port is the in-container port the game listens on.
        Port int `json:"port" yaml:"port"`
        // Proto is "tcp" or "udp" (default tcp).
        Proto string `json:"proto,omitempty" yaml:"proto,omitempty"`
        // Optional is a hint that this port may be absent (query ports that only
        // some game modes open). Optional ports never block a start on conflict.
        Optional bool `json:"optional,omitempty" yaml:"optional,omitempty"`
        // Offset is added to the server's configured game port when resolving this
        // exposure, letting a catalog entry follow a remapped base port
        // (e.g. Offset: 1 -> "game port + 1" for a Steam query port).
        Offset int `json:"offset,omitempty" yaml:"offset,omitempty"`
}

// Resolve returns the concrete host ports for a server configured on gamePort.
// Explicit Port wins; otherwise the port is gamePort + Offset.
func (e ExposedPort) Resolve(gamePort int) int {
        if e.Port > 0 {
                return e.Port
        }
        return gamePort + e.Offset
}

// ProtoOrDefault normalises the transport protocol.
func (e ExposedPort) ProtoOrDefault() string {
        if e.Proto == "udp" {
                return "udp"
        }
        return "tcp"
}

// Catalog is the curated + persisted overlay game list. Reads are safe while
// an administrator adds or removes a custom manifest through the running API.
type Catalog struct {
	mu          sync.RWMutex
	games       map[string]Game
	builtins    map[string]Game
	custom      map[string]Game
	overlayPath string
}

// loadBuiltinGames loads and validates the embedded YAML game definitions.
func loadBuiltinGames() []Game {
        var games []Game
        entries, err := gameConfigs.ReadDir("games")
        if err != nil {
                panic(fmt.Sprintf("failed to read embedded game configs: %v", err))
        }
        for _, entry := range entries {
                if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
                        continue
                }
                data, err := gameConfigs.ReadFile("games/" + entry.Name())
                if err != nil {
                        panic(fmt.Sprintf("failed to read game config %s: %v", entry.Name(), err))
                }
		game, err := ParseGameManifest(data)
		if err != nil {
			panic(fmt.Sprintf("invalid game config %s: %v", entry.Name(), err))
                }
                games = append(games, game)
        }
        return games
}

// BuiltinGames is the curated catalog of popular SteamCMD servers (loaded
// from embedded YAML). Custom entries live in the data directory overlay.
var BuiltinGames = loadBuiltinGames()

// NewCatalog builds a catalog from built-in games and an optional JSON overlay.
// It is retained as a convenience constructor; production startup uses
// LoadCatalog so a malformed overlay is reported rather than silently ignored.
func NewCatalog(overlayPath string) *Catalog {
	catalog, err := LoadCatalog(overlayPath)
	if err == nil {
		return catalog
	}
	return newCatalog(overlayPath)
}

// LoadCatalog loads built-in manifests plus custom game definitions from the
// JSON overlay. Invalid overlay data is an error; callers must not silently
// start with a different catalog than the operator configured.
func LoadCatalog(overlayPath string) (*Catalog, error) {
	catalog := newCatalog(overlayPath)
	if overlayPath == "" {
		return catalog, nil
	}
	data, err := os.ReadFile(overlayPath)
	if errors.Is(err, os.ErrNotExist) {
		return catalog, nil
        }
	if err != nil {
		return nil, fmt.Errorf("read Steam game catalog %q: %w", overlayPath, err)
	}
	if len(data) > 16*MaxGameManifestSize {
		return nil, fmt.Errorf("Steam game catalog %q exceeds the maximum size", overlayPath)
	}
	var entries []Game
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("parse Steam game catalog %q: %w", overlayPath, err)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse Steam game catalog %q: trailing JSON value", overlayPath)
		}
		return nil, fmt.Errorf("parse Steam game catalog %q: %w", overlayPath, err)
	}

	seen := map[string]bool{}
	for _, game := range entries {
		game.Type = "steam_cmd"
		if err := ValidateGame(game); err != nil {
			return nil, fmt.Errorf("invalid custom game %q: %w", game.Slug, err)
		}
		if seen[game.Slug] {
			return nil, fmt.Errorf("duplicate custom game slug %q", game.Slug)
                        }
		seen[game.Slug] = true
		if prior, ok := catalog.games[game.Slug]; ok && prior.AppID != game.AppID {
			return nil, fmt.Errorf("custom game %q changes app_id from %d to %d; override a game only with its existing Steam app ID", game.Slug, prior.AppID, game.AppID)
                }
		for slug, prior := range catalog.games {
			if slug != game.Slug && prior.AppID == game.AppID {
				return nil, fmt.Errorf("Steam app_id %d is already registered by %q", game.AppID, slug)
        }
		}
		catalog.custom[game.Slug] = cloneGame(game)
		catalog.games[game.Slug] = cloneGame(game)
	}
	return catalog, nil
}

func newCatalog(overlayPath string) *Catalog {
	catalog := &Catalog{
		games:       make(map[string]Game, len(BuiltinGames)),
		builtins:    make(map[string]Game, len(BuiltinGames)),
		custom:      map[string]Game{},
		overlayPath: overlayPath,
	}
	for _, game := range BuiltinGames {
		game = cloneGame(game)
		catalog.games[game.Slug] = game
		catalog.builtins[game.Slug] = game
	}
	return catalog
}

// All lists built-in and custom games in stable name/slug order.
func (c *Catalog) All() []Game {
	c.mu.RLock()
	defer c.mu.RUnlock()
        out := make([]Game, 0, len(c.games))
	for _, game := range c.games {
		out = append(out, cloneGame(game))
        }
	sortGames(out)
        return out
}

// CustomAll lists only user-provided overlay definitions.
func (c *Catalog) CustomAll() []Game {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Game, 0, len(c.custom))
	for _, game := range c.custom {
		out = append(out, cloneGame(game))
	}
	sortGames(out)
	return out
}

// IsCustom reports whether slug is provided by the data-directory overlay.
func (c *Catalog) IsCustom(slug string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.custom[slug]
	return ok
}

// BySlug fetches one game.
func (c *Catalog) BySlug(slug string) (Game, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	game, ok := c.games[slug]
	return cloneGame(game), ok
}

// ByAppID fetches one game by its Steam app id.
func (c *Catalog) ByAppID(appID string) (Game, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, game := range c.games {
		if fmt.Sprint(game.AppID) == appID {
			return cloneGame(game), true
                }
        }
        return Game{}, false
}

// Upsert persists a custom game manifest to the data-directory overlay and
// immediately makes it available to creation and server runtime services.
func (c *Catalog) Upsert(game Game) error {
	game.Type = "steam_cmd"
	game.Name = strings.TrimSpace(game.Name)
	if err := ValidateGame(game); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.overlayPath == "" {
		return fmt.Errorf("custom game catalog has no persistent overlay path")
	}
	for slug, prior := range c.games {
		if slug != game.Slug && prior.AppID == game.AppID {
			return fmt.Errorf("Steam app_id %d is already registered by %q", game.AppID, slug)
		}
	}
	if prior, exists := c.builtins[game.Slug]; exists && prior.AppID != game.AppID {
		return fmt.Errorf("built-in game %q cannot be reassigned to app_id %d", game.Slug, game.AppID)
	}

	custom := make(map[string]Game, len(c.custom)+1)
	for slug, entry := range c.custom {
		custom[slug] = cloneGame(entry)
	}
	custom[game.Slug] = cloneGame(game)
	if err := c.writeOverlay(custom); err != nil {
		return err
	}
	c.custom = custom
	c.games[game.Slug] = cloneGame(game)
	return nil
}

// DeleteCustom removes a user-provided definition. Built-in manifests are not
// deletable; removing a custom override of a built-in restores the embedded one.
func (c *Catalog) DeleteCustom(slug string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.custom[slug]; !ok {
		return fmt.Errorf("custom game manifest %q not found", slug)
	}
	if c.overlayPath == "" {
		return fmt.Errorf("custom game catalog has no persistent overlay path")
	}
	custom := make(map[string]Game, len(c.custom)-1)
	for key, game := range c.custom {
		if key != slug {
			custom[key] = cloneGame(game)
		}
	}
	if err := c.writeOverlay(custom); err != nil {
		return err
	}
	c.custom = custom
	if builtin, ok := c.builtins[slug]; ok {
		c.games[slug] = cloneGame(builtin)
	} else {
		delete(c.games, slug)
	}
	return nil
}

func (c *Catalog) writeOverlay(entries map[string]Game) error {
	games := make([]Game, 0, len(entries))
	for _, game := range entries {
		games = append(games, cloneGame(game))
	}
	sortGames(games)
	data, err := json.MarshalIndent(games, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Steam game catalog: %w", err)
	}
	if len(data) > 16*MaxGameManifestSize {
		return fmt.Errorf("Steam game catalog exceeds the maximum size")
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(c.overlayPath), 0o750); err != nil {
		return fmt.Errorf("create Steam game catalog directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(c.overlayPath), ".steamgames-*.tmp")
	if err != nil {
		return fmt.Errorf("create Steam game catalog temp file: %w", err)
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return fmt.Errorf("secure Steam game catalog temp file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write Steam game catalog: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync Steam game catalog: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close Steam game catalog: %w", err)
	}
	if err := os.Rename(tempPath, c.overlayPath); err != nil {
		return fmt.Errorf("replace Steam game catalog: %w", err)
	}
	return nil
}

func cloneGame(game Game) Game {
	game.ConfigFiles = append([]string(nil), game.ConfigFiles...)
	game.SaveDirs = append([]string(nil), game.SaveDirs...)
	game.BackupDirs = append([]string(nil), game.BackupDirs...)
	game.Expose = append([]ExposedPort(nil), game.Expose...)
	if game.Env != nil {
		env := make(map[string][]string, len(game.Env))
		for key, values := range game.Env {
			env[key] = append([]string(nil), values...)
		}
		game.Env = env
	}
	return game
}

func sortGames(games []Game) {
	sort.Slice(games, func(i, j int) bool {
		left, right := strings.ToLower(games[i].Name), strings.ToLower(games[j].Name)
		if left == right {
			return games[i].Slug < games[j].Slug
		}
		return left < right
	})
}

// Manager wraps the steamcmd binary lifecycle.
type Manager struct {
        installDir  string
        http        *http.Client
        docker      *dockerx.Manager // optional: run steamcmd jobs in containers
        dockerImage string
}

// NewManager builds the manager; installDir hosts the steamcmd binary.
func NewManager(installDir string) *Manager {
        return &Manager{installDir: installDir, http: &http.Client{Timeout: 10 * time.Minute},
                dockerImage: "steamcmd/steamcmd:latest"}
}

// SetDocker enables containerized steamcmd jobs (preferred when the docker
// execution backend is available — works on hosts without steamcmd/32-bit
// libs, e.g. arm64 or minimal images).
func (m *Manager) SetDocker(d *dockerx.Manager, image string) {
        m.docker = d
        if image != "" {
                m.dockerImage = image
        }
}

// DockerEnabled reports whether containerized steamcmd is active.
func (m *Manager) DockerEnabled() bool {
        return m.docker != nil && m.docker.Available()
}

// BinaryPath returns the steamcmd executable to use for LOCAL runs.
// Preference: the managed copy in installDir, then a system-wide install
// (/opt/steamcmd in the servy panel image, /usr/games/steamcmd for distro
// packages) — reusing the pre-seeded binary avoids a duplicate download into
// the data dir.
func (m *Manager) BinaryPath() string {
        if runtime.GOOS == "windows" {
                return filepath.Join(m.installDir, "steamcmd.exe")
        }
        local := filepath.Join(m.installDir, "steamcmd.sh")
        if _, err := os.Stat(local); err == nil {
                return local
        }
        for _, sys := range []string{"/opt/steamcmd/steamcmd.sh", "/usr/games/steamcmd"} {
                if st, err := os.Stat(sys); err == nil && !st.IsDir() {
                        return sys
                }
        }
        return local
}

// Installed reports whether a usable steamcmd binary exists (managed copy or
// a system-wide install).
func (m *Manager) Installed() bool {
        if runtime.GOOS == "windows" {
                _, err := os.Stat(filepath.Join(m.installDir, "steamcmd.exe"))
                return err == nil
        }
        if _, err := os.Stat(filepath.Join(m.installDir, "steamcmd.sh")); err == nil {
                return true
        }
        for _, sys := range []string{"/opt/steamcmd/steamcmd.sh", "/usr/games/steamcmd"} {
                if st, err := os.Stat(sys); err == nil && !st.IsDir() {
                        return true
                }
        }
        return false
}

// Install downloads & extracts SteamCMD for the current OS.
func (m *Manager) Install() error {
        if m.Installed() {
                return nil
        }
        os.MkdirAll(m.installDir, 0o755)
        if runtime.GOOS == "windows" {
                // steamcmd.zip with steamcmd.exe
                resp, err := m.http.Get("https://steamcdn-a.akamaihd.net/client/installer/steamcmd.zip")
                if err != nil {
                        return err
                }
                defer resp.Body.Close()
                tmp := filepath.Join(m.installDir, "steamcmd.zip")
                out, err := os.Create(tmp)
                if err != nil {
                        return err
                }
                io.Copy(out, resp.Body)
                out.Close()
                defer os.Remove(tmp)
                return unzipFile(tmp, m.installDir)
        }
        // linux/mac: steamcmd_linux.tar.gz
        resp, err := m.http.Get("https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz")
        if err != nil {
                return err
        }
        defer resp.Body.Close()
        gz, err := gzip.NewReader(resp.Body)
        if err != nil {
                return err
        }
        defer gz.Close()
        tr := tar.NewReader(gz)
        for {
                hdr, err := tr.Next()
                if err == io.EOF {
                        break
                }
                if err != nil {
                        return err
                }
                name := filepath.Clean(hdr.Name)
                if strings.Contains(name, "..") {
                        continue
                }
                target := filepath.Join(m.installDir, name)
                if hdr.Typeflag == tar.TypeDir {
                        os.MkdirAll(target, 0o755)
                        continue
                }
                out, err := os.Create(target)
                if err != nil {
                        return err
                }
                io.Copy(out, tr)
                out.Close()
                if name == "steamcmd.sh" {
                        os.Chmod(target, 0o755)
                }
        }
        return nil
}

// platformArgs returns the SteamCMD launcher directive that forces the depot
// platform. Windows-only servers (Abiotic Factor, Enshrouded, ...) ship no
// Linux build, so the download must target the Windows depot explicitly.
func platformArgs(platform string) []string {
        switch strings.ToLower(strings.TrimSpace(platform)) {
        case "windows":
                return []string{"+@sSteamCmdForcePlatformType", "windows"}
        case "linux":
                return []string{"+@sSteamCmdForcePlatformType", "linux"}
        }
        return nil
}

// dockerEnvForPlatform returns environment variables for the Docker container
// to force SteamCMD to use a specific depot platform. The
// SteamCmdForcePlatformType environment variable is the officially supported
// way to force a depot platform in SteamCMD (the +@ directive is not reliably
// passed through the official steamcmd/steamcmd:latest entrypoint script).
func (m *Manager) dockerEnvForPlatform(platform string) []string {
        env := []string{"TERM=xterm"}
        switch strings.ToLower(strings.TrimSpace(platform)) {
        case "windows":
                env = append(env, "SteamCmdForcePlatformType=windows")
        case "linux":
                env = append(env, "SteamCmdForcePlatformType=linux")
        }
        return env
}

// Run executes steamcmd with the given arguments (anonymous login).
// streamOut receives stdout lines (for the installer progress UI).
func (m *Manager) Run(args []string, workDir string, streamOut func(line string)) error {
        return m.RunPlatform("", args, workDir, streamOut)
}

// RunPlatform is Run with an explicit depot platform override ("" = host default).
func (m *Manager) RunPlatform(platform string, args []string, workDir string, streamOut func(line string)) error {
        if !m.Installed() {
                if err := m.Install(); err != nil {
                        return fmt.Errorf("steamcmd install failed: %w", err)
                }
        }
        // Order: platform directive first, then force_install_dir, then
        // +login anonymous (inserted by buildSteamCmdArgs), then the remaining
        // arguments. SteamCMD parses launch options left to right, so the
        // install dir must be set before the login switches the context.
        full := append([]string{}, platformArgs(platform)...)
        full = append(full, buildSteamCmdArgs(args)...)
        full = append(full, "+quit")
        var cmd *exec.Cmd
        if runtime.GOOS == "windows" {
                cmd = exec.Command(m.BinaryPath(), full...)
        } else {
                cmd = exec.Command("/bin/bash", append([]string{m.BinaryPath()}, full...)...)
        }
        cmd.Dir = workDir
        if streamOut != nil {
                // Merge stdout+stderr into one piped stream. The previous code did
                // `cmd.Stderr = cmd.Stdout.(*os.File)` on a nil interface (StdoutPipe
                // does not populate cmd.Stdout), which panicked the panel on every
                // local steamcmd run with streaming enabled.
                pr, pw := io.Pipe()
                cmd.Stdout = pw
                cmd.Stderr = pw
                go func() {
                        buf := make([]byte, 4096)
                        for {
                                n, rerr := pr.Read(buf)
                                if n > 0 {
                                        for _, line := range strings.Split(string(buf[:n]), "\n") {
                                                if line != "" {
                                                        streamOut(line)
                                                }
                                        }
                                }
                                if rerr != nil {
                                        return
                                }
                        }
                }()
                // Ensure the reader goroutine sees EOF once the command has exited.
                defer pw.Close()
        }
        return cmd.Run()
}

// RunDocker runs steamcmd args in a one-shot official steamcmd container with
// the host install dir mounted at /data. Preferred path when the docker
// execution backend is available (no host steamcmd / 32-bit libs needed).
func (m *Manager) RunDocker(args []string, hostInstallDir string, streamOut func(line string)) error {
        return m.RunDockerPlatform("", args, hostInstallDir, streamOut)
}

// RunDockerPlatform is RunDocker with an explicit depot platform override.
func (m *Manager) RunDockerPlatform(platform string, args []string, hostInstallDir string, streamOut func(line string)) error {
        if !m.DockerEnabled() {
                return fmt.Errorf("docker steamcmd requested but daemon unavailable")
        }
        // Translate container path to host path if running inside a container
        // (sibling containers bind-mount HOST paths — servy's own /data view
        // does not exist on the host).
        abs, err := dockerx.HostBindPath(hostInstallDir)
        if err != nil {
                return err
        }
        // Order: platform directive first (the +@ directive is authoritative and
        // independent of the image entrypoint), then force_install_dir, then
        // login, then app_update. The SteamCmdForcePlatformType env stays set as
        // a belt-and-suspenders fallback.
        full := append([]string{}, platformArgs(platform)...)
        full = append(full, buildSteamCmdArgs(args)...)
        full = append(full, "+quit")
        spec := dockerx.RunSpec{
                Image:      m.dockerImage,
                Cmd:        full,
                Env:        m.dockerEnvForPlatform(platform),
                WorkingDir: "/data",
                Binds:      []string{abs + ":/data"},
                // Run the job with servy's own uid:gid so it can write the
                // bind-mounted install dir (official image user uid 10000
                // cannot write host-owned directories).
                User: dockerx.CurrentUserSpec(),
        }
        ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
        defer cancel()
        res, err := m.docker.RunJob(ctx, spec, streamOut)
        if err != nil {
                return err
        }
        if res.ExitCode != 0 {
                var msg string
                if len(res.Output) > 0 {
                        // include last 50 lines of output in error
                        start := len(res.Output) - 50
                        if start < 0 {
                                start = 0
                        }
                        msg = fmt.Sprintf("steamcmd container exited %d: %s", res.ExitCode, strings.Join(res.Output[start:], "\n"))
                } else {
                        msg = fmt.Sprintf("steamcmd container exited %d", res.ExitCode)
                }
                return fmt.Errorf("%s", msg)
        }
        return nil
}

// buildSteamCmdArgs inserts the anonymous login into a raw steamcmd argument
// list: [+force_install_dir, <dir>, +app_update, appID, validate] →
// [+force_install_dir, <dir>, +login, anonymous, +app_update, ...].
// The install-dir token must appear exactly once — a duplicated token used to
// leak into +login as a bogus password argument.
func buildSteamCmdArgs(args []string) []string {
        full := make([]string, 0, len(args)+2)
        for i := 0; i < len(args); i++ {
                full = append(full, args[i])
                if args[i] == "+force_install_dir" && i+1 < len(args) {
                        i++ // consume the path argument — appended once, here
                        full = append(full, args[i], "+login", "anonymous")
                }
        }
        if !hasLogin(full) {
                // no force_install_dir in args: prepend the login
                full = append([]string{"+login", "anonymous"}, full...)
        }
        return full
}

// hasLogin reports whether args contains the +login directive.
func hasLogin(args []string) bool {
        for _, a := range args {
                if a == "+login" {
                        return true
                }
        }
        return false
}

// InstallApp installs/updates a game server into installDir.
// Prefers local steamcmd when running in container (with 32-bit libs),
// falls back to Docker, then to local binary. platform forces the SteamCMD
// depot platform ("" = host default, "windows" for Windows-only servers).
func (m *Manager) InstallApp(appID, installDir string, streamOut func(string)) error {
        return m.InstallAppPlatform("", appID, installDir, streamOut)
}

// InstallAppPlatform is InstallApp with an explicit depot platform override.
func (m *Manager) InstallAppPlatform(platform, appID, installDir string, streamOut func(string)) error {
        os.MkdirAll(installDir, 0o755)
        dockerArgs := []string{"+force_install_dir", "/data", "+app_update", appID, "validate"}
        localArgs := []string{"+force_install_dir", installDir, "+app_update", appID, "validate"}

        // If running in container with local steamcmd, prefer it (no bind mount issues)
        if isRunningInContainer() && (m.Installed() || m.Install() == nil) {
                if err := m.RunPlatform(platform, localArgs, m.installDir, streamOut); err != nil {
                        // fallback to Docker if local fails
                        if m.DockerEnabled() {
                                return m.RunDockerPlatform(platform, dockerArgs, installDir, streamOut)
                        }
                        return err
                }
                return nil
        }

        // Not in container: prefer Docker, fallback to local
        if m.DockerEnabled() {
                if err := m.RunDockerPlatform(platform, dockerArgs, installDir, streamOut); err != nil {
                        if m.Installed() || m.Install() == nil {
                                return m.RunPlatform(platform, localArgs, m.installDir, streamOut)
                        }
                        return err
                }
                return nil
        }
        if m.Installed() || m.Install() == nil {
                return m.RunPlatform(platform, localArgs, m.installDir, streamOut)
        }
        return fmt.Errorf("steamcmd not installed and docker unavailable")
}

// InstallWorkshopItem downloads a workshop item into contentDir.
func (m *Manager) InstallWorkshopItem(appID, workshopID, contentDir string, streamOut func(string)) error {
        os.MkdirAll(contentDir, 0o755)

        // If running in container with local steamcmd, prefer it
        if isRunningInContainer() && (m.Installed() || m.Install() == nil) {
                if err := m.Run([]string{"+force_install_dir", contentDir, "+workshop_download_item", appID, workshopID}, m.installDir, streamOut); err != nil {
                        if m.DockerEnabled() {
                                return m.RunDocker([]string{"+force_install_dir", "/data", "+workshop_download_item", appID, workshopID}, contentDir, streamOut)
                        }
                        return err
                }
                return nil
        }

        // Not in container: prefer Docker, fallback to local
        if m.DockerEnabled() {
                if err := m.RunDocker([]string{"+force_install_dir", "/data", "+workshop_download_item", appID, workshopID}, contentDir, streamOut); err != nil {
                        if m.Installed() || m.Install() == nil {
                                return m.Run([]string{"+force_install_dir", contentDir, "+workshop_download_item", appID, workshopID}, m.installDir, streamOut)
                        }
                        return err
                }
                return nil
        }
        if m.Installed() || m.Install() == nil {
                return m.Run([]string{"+force_install_dir", contentDir, "+workshop_download_item", appID, workshopID}, m.installDir, streamOut)
        }
        return fmt.Errorf("steamcmd not installed and docker unavailable")
}

func unzipFile(zipPath, dest string) error {
        za := zipOpen(zipPath)
        defer za.Close()
        return za.Extract(dest)
}

func jsonUnmarshal(data []byte, v interface{}) error {
        return jsonDecode(data, v)
}

// isRunningInContainer checks if the current process is running inside a
// container. Delegates to the shared dockerx helper (kept for the local
// "prefer host steamcmd inside the servy image" fast path).
func isRunningInContainer() bool {
        return dockerx.InContainer()
}