// Package steamcmd manages the SteamCMD binary and runs installs/updates for
// ANY Steam game server (not just Minecraft), with a curated game catalog
// (name, app id, default run command, config descriptors) + user overlay.
package steamcmd

import (
        "archive/tar"
        "compress/gzip"
        "context"
        "embed"
        "fmt"
        "github.com/FlameInTheDark/servy/internal/defaults"
        "github.com/FlameInTheDark/servy/internal/dockerx"
        "io"
        "net/http"
        "os"
        "os/exec"
        "path/filepath"
        "runtime"
        "strings"
        "time"

        "gopkg.in/yaml.v3"
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
        ConfigFiles []string            `json:"config_files,omitempty" yaml:"config_files,omitempty"` // editable config files (descriptors)
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

// Catalog is the curated + overlay game list.
type Catalog struct{ games map[string]Game }

// loadBuiltinGames loads games from embedded YAML files.
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
                var game Game
                if err := yaml.Unmarshal(data, &game); err != nil {
                        panic(fmt.Sprintf("failed to parse game config %s: %v", entry.Name(), err))
                }
                game.Type = "steam_cmd"
                games = append(games, game)
        }
        return games
}

// BuiltinGames is the curated catalog of popular SteamCMD servers (loaded from embedded YAML).
var BuiltinGames = loadBuiltinGames()

// NewCatalog builds the catalog keyed by slug with overlay support
// (extra games loaded from <data>/steamgames.json — user extensibility).
func NewCatalog(overlayPath string) *Catalog {
        c := &Catalog{games: map[string]Game{}}
        for _, g := range BuiltinGames {
                c.games[g.Slug] = g
        }
        if data, err := os.ReadFile(overlayPath); err == nil {
                var extra []Game
                if jsonUnmarshal(data, &extra) == nil {
                        for _, g := range extra {
                                g.Type = "steam_cmd"
                                c.games[g.Slug] = g
                        }
                }
        }
        return c
}

// All lists catalog games.
func (c *Catalog) All() []Game {
        out := make([]Game, 0, len(c.games))
        for _, g := range c.games {
                out = append(out, g)
        }
        return out
}

// BySlug fetches one game.
func (c *Catalog) BySlug(slug string) (Game, bool) {
        g, ok := c.games[slug]
        return g, ok
}

// ByAppID fetches one game by its Steam app id.
func (c *Catalog) ByAppID(appID string) (Game, bool) {
        for _, g := range c.games {
                if fmt.Sprint(g.AppID) == appID {
                        return g, true
                }
        }
        return Game{}, false
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