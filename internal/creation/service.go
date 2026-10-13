// Package creation implements the server creation wizard backend: zip import,
// jar download (BigBucket), raw executable, SteamCMD game install and the
// legacy panel database importer (internal/database).
package creation

import (
        "archive/zip"
        "encoding/json"
        "errors"
        "fmt"
        "io"
        "net/http"
        "os"
        "path/filepath"
        "sort"
        "strings"
        "time"

        "github.com/google/uuid"

        "github.com/FlameInTheDark/servy/internal/backups"
        "github.com/FlameInTheDark/servy/internal/bigbucket"
        "github.com/FlameInTheDark/servy/internal/database"
        "github.com/FlameInTheDark/servy/internal/events"
        "github.com/FlameInTheDark/servy/internal/files"
        "github.com/FlameInTheDark/servy/internal/logging"
        "github.com/FlameInTheDark/servy/internal/repo/gen"
        "github.com/FlameInTheDark/servy/internal/servers"
        "github.com/FlameInTheDark/servy/internal/steamcmd"
)

// Create types.
const (
        CreateImportZip = "import_zip"
        CreateImportJar = "import_jar"
        CreateDownload  = "download_jar"
        CreateRawExec   = "raw_exec"
        CreateSteamGame = "steam_game"
)

// Request is the wizard payload.
type Request struct {
        CreateType string `json:"create_type"`
        Name       string `json:"name"`
        ServerType string `json:"type"` // minecraft-java | minecraft-bedrock | steam_cmd | custom ...
        Port       int    `json:"port"`
        // import_zip / import_jar
        ArchivePath      string `json:"archive_path,omitempty"` // spool/import location
        Executable       string `json:"executable,omitempty"`
        ExecutionCommand string `json:"execution_command,omitempty"`
        StopCommand      string `json:"stop_command,omitempty"`
        // java memory
        MinMem string `json:"min_mem,omitempty"`
        MaxMem string `json:"max_mem,omitempty"`
        // download_jar
        JarURL  string `json:"jar_url,omitempty"`
        JarName string `json:"jar_name,omitempty"`
        // steam_game
        SteamSlug string `json:"steam_slug,omitempty"`
        AppID     string `json:"app_id,omitempty"`
        // execution environment (process | docker)
        JavaSelection    string            `json:"java_selection,omitempty"`    // "", "java" or JVM path
        Shell            string            `json:"shell,omitempty"`             // "", auto, bash, sh, cmd, powershell, pwsh, git-bash, none
        ExecutionBackend string            `json:"execution_backend,omitempty"` // "process" (default) | "docker"
        DockerImage      string            `json:"docker_image,omitempty"`
        DockerEnv        map[string]string `json:"docker_env,omitempty"`
        DockerPorts      []string          `json:"docker_ports,omitempty"` // "host:container/proto"
        DockerBinds      []string          `json:"docker_binds,omitempty"` // extra mounts
        DockerNetwork    string            `json:"docker_network,omitempty"`
        DockerWorkdir    string            `json:"docker_workdir,omitempty"` // container workdir (default /data)
        DockerAutoremove bool              `json:"docker_autoremove,omitempty"`
        // roles to grant access
        Roles []int64 `json:"roles,omitempty"`
        // misc
        AutoStart      bool `json:"auto_start"`
        CrashDetection bool `json:"crash_detection"`
}

// Result describes the created server + any follow-ups.
type Result struct {
        ServerID         string   `json:"server_id"`
        Name             string   `json:"name"`
        Type             string   `json:"type"`
        Executable       string   `json:"executable"`
        ExecutionCommand string   `json:"execution_command"`
        DetectedExes     []string `json:"detected_executables,omitempty"`
        NeedsConfirm     bool     `json:"needs_confirm"`
        SteamOutput      []string `json:"steam_output,omitempty"`
}

// Service performs creation flows.
type Service struct {
        db          *database.Queries
        log         *logging.Manager
        bus         *events.Bus
        reg         *servers.Registry
        bucket      *bigbucket.Client
        steam       *steamcmd.Manager
        catalog     *steamcmd.Catalog
        backupSvc   *backups.Service
        serversRoot string
        importDir   string
        http        *http.Client
}

// New builds the service.
func New(db *database.Queries, log *logging.Manager, bus *events.Bus, reg *servers.Registry,
        bucket *bigbucket.Client, steam *steamcmd.Manager, catalog *steamcmd.Catalog,
        backupSvc *backups.Service, serversRoot, importDir string) *Service {
        return &Service{db: db, log: log, bus: bus, reg: reg, bucket: bucket, steam: steam,
                catalog: catalog, backupSvc: backupSvc, serversRoot: serversRoot, importDir: importDir,
                http: &http.Client{Timeout: 15 * time.Minute}}
}

// Validate checks a wizard request.
func (s *Service) Validate(r Request) error {
        switch r.CreateType {
        case CreateImportZip, CreateImportJar, CreateDownload, CreateRawExec, CreateSteamGame:
        default:
                return fmt.Errorf("invalid create_type %q", r.CreateType)
        }
        if strings.TrimSpace(r.Name) == "" {
                return errors.New("server name required")
        }
        if r.CreateType == CreateSteamGame {
                if r.SteamSlug == "" && r.AppID == "" {
                        return errors.New("steam_slug or app_id required")
                }
        }
        if r.CreateType == CreateImportZip && r.ArchivePath == "" {
                return errors.New("archive_path required")
        }
        if r.CreateType == CreateDownload && r.JarURL == "" {
                return errors.New("jar_url required")
        }
        return nil
}

// Create executes the wizard flow.
func (s *Service) Create(r Request, userID int64) (*Result, error) {
        if err := s.Validate(r); err != nil {
                return nil, err
        }
        serverID := uuid.NewString()
        dir := filepath.Join(s.serversRoot, serverID)
        if err := os.MkdirAll(dir, 0o755); err != nil {
                return nil, err
        }
        res := &Result{ServerID: serverID, Name: r.Name}

        serverType := r.ServerType
        def := gen.Server{
                ServerID: serverID, ServerName: servers.SanitizeServerName(r.Name), Path: dir,
                ServerIp: "127.0.0.1", ServerPort: int64(defaultPort(serverType, r.Port)),
                Type: serverType, AutoStart: b2i(r.AutoStart), CrashDetection: b2i(r.CrashDetection),
                AutoStartDelay: 10, ShutdownTimeout: 60, ShowStatus: 1, CountPlayers: 1,
                StopCommand: orDefault(r.StopCommand, "stop"), IoMode: "pipes", StopAction: "command",
                CreatedBy: &userID, JavaSelection: r.JavaSelection, Shell: r.Shell,
        }
        // execution environment
        if r.ExecutionBackend == "docker" {
                def.ExecutionBackend = "docker"
                if r.DockerImage != "" {
                        def.DockerImage = &r.DockerImage
                }
                if len(r.DockerEnv) > 0 {
                        if b, err := json.Marshal(r.DockerEnv); err == nil {
                                envJSON := string(b)
                                def.DockerEnv = &envJSON
                        }
                }
                if len(r.DockerPorts) > 0 {
                        if b, err := json.Marshal(r.DockerPorts); err == nil {
                                portsJSON := string(b)
                                def.DockerPorts = &portsJSON
                        }
                }
                if len(r.DockerBinds) > 0 {
                        if b, err := json.Marshal(r.DockerBinds); err == nil {
                                bindsJSON := string(b)
                                def.DockerBinds = &bindsJSON
                        }
                }
                if r.DockerNetwork != "" {
                        def.DockerNetwork = &r.DockerNetwork
                }
                if r.DockerWorkdir != "" {
                        def.DockerWorkdir = &r.DockerWorkdir
                }
                def.DockerAutoremove = b2i(r.DockerAutoremove)
        }

        switch r.CreateType {
        case CreateImportZip:
                src := r.ArchivePath
                if !filepath.IsAbs(src) {
                        src = filepath.Join(s.importDir, src)
                }
                if err := s.extractArchive(r.ArchivePath, dir); err != nil {
                        os.RemoveAll(dir)
                        os.Remove(src) // cleanup uploaded archive
                        return nil, err
                }
                os.Remove(src) // cleanup uploaded archive on success
                exes := detectExecutables(dir, serverType)
                res.DetectedExes = exes
                if r.Executable != "" {
                        def.Executable = r.Executable
                        def.ExecutionCommand = r.ExecutionCommand
                } else if len(exes) > 0 {
                        def.Executable = exes[0]
                        def.ExecutionCommand = buildCommand(serverType, exes[0], r)
                } else {
                        res.NeedsConfirm = true
                }
        case CreateImportJar:
                // archive contains a runnable jar layout
                src := r.ArchivePath
                if !filepath.IsAbs(src) {
                        src = filepath.Join(s.importDir, src)
                }
                if err := s.extractArchive(r.ArchivePath, dir); err != nil {
                        os.RemoveAll(dir)
                        os.Remove(src) // cleanup uploaded archive
                        return nil, err
                }
                os.Remove(src) // cleanup uploaded archive on success
                jar := r.Executable
                if jar == "" {
                        exes := detectExecutables(dir, "minecraft-java")
                        if len(exes) == 0 {
                                os.RemoveAll(dir)
                                return nil, errors.New("no executable found in archive")
                        }
                        jar = exes[0]
                }
                def.Executable = jar
                def.ExecutionCommand = buildCommand("minecraft-java", jar, r)
        case CreateDownload:
                jarName := r.JarName
                if jarName == "" {
                        jarName = filepath.Base(r.JarURL)
                }
                dest := filepath.Join(dir, jarName)
                if _, err := files.DownloadURL(r.JarURL, dest); err != nil {
                        os.RemoveAll(dir)
                        return nil, fmt.Errorf("download failed: %v", err)
                }
                def.Executable = jarName
                def.ExecutionCommand = buildCommand("minecraft-java", jarName, r)
                def.ExecutableUpdateUrl = &r.JarURL
        case CreateRawExec:
                if r.Executable == "" || r.ExecutionCommand == "" {
                        os.RemoveAll(dir)
                        return nil, errors.New("executable and execution_command required")
                }
                def.Executable = r.Executable
                def.ExecutionCommand = r.ExecutionCommand
                if serverType == "" {
                        def.Type = "custom"
                }
        case CreateSteamGame:
                game, err := s.resolveGame(r)
                if err != nil {
                        os.RemoveAll(dir)
                        return nil, err
                }
                s.log.Get("server").Info("installing steam app %d (%s) into %s", game.AppID, game.Name, dir)
                out := []string{}
                // Platform forces the SteamCMD depot (windows for Windows-only servers).
                err = s.steam.InstallAppPlatform(game.Platform, fmt.Sprint(game.AppID), dir, func(line string) {
                        out = append(out, line)
                        if len(out) > 500 {
                                out = out[len(out)-500:]
                        }
                        // publish live output to UI
                        s.bus.Publish(events.Event{
                                Type:     events.EvSteamCmdOutput,
                                ServerID: serverID,
                                Data:     map[string]string{"line": line},
                        })
                })
                res.SteamOutput = out
                if err != nil {
                        os.RemoveAll(dir)
                        return nil, fmt.Errorf("steamcmd failed: %v", err)
                }
                def.Type = "steam_cmd"
                def.AppID = ptr(fmt.Sprint(game.AppID))
                def.Executable = game.Executable
                port := r.Port
                if port == 0 {
                        port = game.Port
                }
                def.ServerPort = int64(port)
                // Query port: catalog override, else the Source convention (game port + 1).
                queryPort := game.QueryPort
                if queryPort == 0 {
                        queryPort = port + 1
                }
                cmd := strings.ReplaceAll(game.RunCommand, "{SERVER_NAME}", r.Name)
                cmd = strings.ReplaceAll(cmd, "{PORT}", fmt.Sprint(port))
                cmd = strings.ReplaceAll(cmd, "{QUERY_PORT}", fmt.Sprint(queryPort))
                // Use server's data directory for saves (persists in Docker volume)
                saveDir := filepath.Join(dir, "saves")
                cmd = strings.ReplaceAll(cmd, "{SAVE_DIR}", saveDir)
                def.ExecutionCommand = cmd
                def.StopCommand = orDefault(game.StopCommand, "quit")
                def.StopAction = "command"
                // env.json seed (e.g. LD_LIBRARY_PATH for valheim)
                if len(game.Env) > 0 {
                        envSpec := map[string]map[string]interface{}{}
                        for k, v := range game.Env {
                                mode := "append"
                                vals := v
                                if len(vals) == 2 && strings.HasPrefix(vals[1], "mode:") {
                                        mode = strings.TrimPrefix(vals[1], "mode:")
                                        vals = vals[:1]
                                }
                                envSpec[k] = map[string]interface{}{"contents": vals, "mode": mode}
                        }
                        if b, err := json.MarshalIndent(envSpec, "", "  "); err == nil {
                                os.WriteFile(filepath.Join(dir, "env.json"), b, 0o644)
                        }
                }
                res.Type = "steam_cmd"

                // Windows-only games must run in a sibling container (Wine image) — the
                // host process backend cannot execute them on Linux. The catalog ships a
                // default image so the user does not have to supply one; an explicit
                // user-supplied image always wins.
                if game.WindowsOnly {
                        def.ExecutionBackend = "docker"
                        if def.DockerImage == nil || *def.DockerImage == "" {
                                if game.DockerImage != "" {
                                        img := game.DockerImage
                                        def.DockerImage = &img
                                } else {
                                        // generic Windows-only game: fall back to the repo's
                                        // Wine image so the server can start out of the box
                                        img := steamcmd.DefaultWineImage
                                        def.DockerImage = &img
                                }
                                res.NeedsConfirm = true
                                res.DetectedExes = append(res.DetectedExes,
                                        "windows-only game: starts in the Wine container "+*def.DockerImage+
                                                " — override docker_image in server settings to use your own")
                        }
                }
                // Any steam game on the docker backend without an explicit image gets
                // the built-in default too (native Linux servers included — valheim,
                // cs2, ...): users should never have to type an image reference.
                if def.ExecutionBackend == "docker" && (def.DockerImage == nil || *def.DockerImage == "") {
                        img := steamcmd.DefaultWineImage
                        def.DockerImage = &img
                }
        }
        if def.Type == "" {
                def.Type = serverType
        }
        if def.Type == "" {
                def.Type = "custom"
        }

        sup, err := s.reg.Create(def)
        if err != nil {
                os.RemoveAll(dir)
                return nil, err
        }
        // default backup config
        _ = s.backupSvc.EnsureDefault(sup.Definition(), filepath.Join(filepath.Dir(s.serversRoot), "backups"))
        // grant roles
        for _, roleID := range r.Roles {
                perms := "11000000" // COMMANDS + TERMINAL default grant
                s.db.SetRoleServer(roleID, serverID, perms)
        }
        res.Name = sup.Definition().ServerName
        res.Executable = sup.Definition().Executable
        res.ExecutionCommand = sup.Definition().ExecutionCommand
        res.Type = sup.Definition().Type
        s.log.Audit("", userID, serverID, "", fmt.Sprintf("Created server %s (%s, %s)", r.Name, res.Type, r.CreateType))
        s.bus.Publish(events.Event{Type: events.EvSendStartReload, ServerID: serverID,
                Data: map[string]interface{}{"server_id": serverID, "created": true}})
        return res, nil
}

func (s *Service) resolveGame(r Request) (steamcmd.Game, error) {
        if r.SteamSlug != "" {
                if g, ok := s.catalog.BySlug(r.SteamSlug); ok {
                        return g, nil
                }
        }
        if r.AppID != "" {
                if g, ok := s.catalog.ByAppID(r.AppID); ok {
                        return g, nil
                }
                // unknown app id: generic steam server
                return steamcmd.Game{
                        AppID: atoi(r.AppID), Name: r.Name, Slug: fmt.Sprintf("app-%s", r.AppID),
                        Type: "steam_cmd", RunCommand: "./run.sh", Executable: "run.sh",
                        StopCommand: "quit", Port: 27015, PortName: "game",
                }, nil
        }
        return steamcmd.Game{}, errors.New("unknown steam game")
}

// extractArchive validates + extracts a zip, unwrapping a single top dir.
// archivePath may be absolute anywhere on the host (the wizard explicitly
// offers "absolute archive path on the host") or relative to the import dir.
func (s *Service) extractArchive(archivePath, dest string) error {
        src := archivePath
        if !filepath.IsAbs(src) {
                // relative → inside import dir
                src = filepath.Join(s.importDir, src)
        }
        if _, err := os.Stat(src); err != nil {
                return fmt.Errorf("archive not found: %s", src)
        }
        zr, err := zip.OpenReader(src)
        if err != nil {
                return fmt.Errorf("cannot open archive: %v", err)
        }
        defer zr.Close()
        // traversal validation + root detection
        rootSet := map[string]bool{}
        for _, f := range zr.File {
                name := filepath.Clean(f.Name)
                if strings.Contains(name, "..") {
                        s.log.Get("server").Error("TRAVERSAL DETECTED in import archive: %s", f.Name)
                        return files.ErrTraversal
                }
                parts := strings.SplitN(filepath.ToSlash(f.Name), "/", 2)
                if len(parts) > 0 && parts[0] != "" {
                        rootSet[parts[0]] = true
                }
        }
        // Only unwrap when the single root is a DIRECTORY (some entry lives
        // below it). A single top-level FILE (e.g. a zip containing just
        // server.jar) must be extracted as-is — previously it was swallowed
        // by the unwrap logic and the server ended up empty.
        unwrap := false
        if len(rootSet) == 1 {
                var root string
                for k := range rootSet {
                        root = k
                }
                for _, f := range zr.File {
                        if strings.HasPrefix(filepath.ToSlash(f.Name), root+"/") {
                                unwrap = true
                                break
                        }
                }
        }
        for _, f := range zr.File {
                name := filepath.Clean(f.Name)
                rel := name
                if unwrap {
                        rel = strings.TrimPrefix(strings.TrimPrefix(filepath.ToSlash(f.Name), strings.SplitN(filepath.ToSlash(f.Name), "/", 2)[0]), "/")
                        rel = filepath.FromSlash(rel)
                }
                if rel == "" || rel == "." {
                        continue
                }
                target := filepath.Join(dest, rel)
                if f.FileInfo().IsDir() {
                        os.MkdirAll(target, 0o755)
                        continue
                }
                os.MkdirAll(filepath.Dir(target), 0o755)
                rc, err := f.Open()
                if err != nil {
                        continue
                }
                out, err := os.Create(target)
                if err != nil {
                        rc.Close()
                        continue
                }
                io.Copy(out, rc)
                out.Close()
                rc.Close()
                mode := f.Mode()
                if mode&0o100 != 0 { // executable bit from zip
                        os.Chmod(target, 0o755)
                }
        }
        return nil
}

// detectExecutables finds candidate executables (jars, scripts, bins).
func detectExecutables(dir, serverType string) []string {
        var out []string
        filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
                if err != nil || info.IsDir() {
                        return nil
                }
                rel, _ := filepath.Rel(dir, p)
                // skip deep dependency dirs
                if strings.HasPrefix(rel, "libraries/") || strings.HasPrefix(rel, "mods/") ||
                        strings.HasPrefix(rel, "cache/") || strings.HasPrefix(rel, "logs/") {
                        return nil
                }
                n := strings.ToLower(info.Name())
                switch {
                case strings.HasSuffix(n, ".jar"):
                        if strings.Contains(n, "forge") || strings.Contains(n, "server") ||
                                strings.Contains(n, "paper") || strings.Contains(n, "spigot") ||
                                strings.Contains(n, "bukkit") || strings.Contains(n, "fabric") ||
                                strings.Contains(n, "neo") || !strings.Contains(n, "installer") {
                                out = append(out, rel)
                        }
                case n == "bedrock_server" || n == "bedrock_server.exe" || n == "hs_err_pid*":
                        out = append(out, rel)
                case strings.HasSuffix(n, ".sh") || strings.HasSuffix(n, ".bat"):
                        out = append(out, rel)
                case info.Mode()&0o111 != 0 && !strings.Contains(n, "."):
                        out = append(out, rel)
                }
                return nil
        })
        sort.Strings(out)
        if len(out) > 20 {
                out = out[:20]
        }
        return out
}

// buildCommand crafts the execution command for java-style servers.
func buildCommand(serverType, exe string, r Request) string {
        minMem := orDefault(r.MinMem, "128M")
        maxMem := orDefault(r.MaxMem, "256M")
        switch serverType {
        case "minecraft-java":
                if strings.HasSuffix(strings.ToLower(exe), ".sh") || strings.HasSuffix(strings.ToLower(exe), ".bat") {
                        return "./" + exe
                }
                return fmt.Sprintf("java -Xms%s -Xmx%s -jar %s nogui", minMem, maxMem, exe)
        case "minecraft-bedrock":
                if exe == "" {
                        return "./bedrock_server"
                }
                if strings.HasPrefix(exe, "./") || strings.HasPrefix(exe, ".\\") {
                        return exe
                }
                return "./" + exe
        default:
                if r.ExecutionCommand != "" {
                        return r.ExecutionCommand
                }
                return "./" + exe
        }
}

func defaultPort(serverType string, port int) int {
        if port != 0 {
                return port
        }
        switch serverType {
        case "minecraft-java":
                return 25565
        case "minecraft-bedrock":
                return 19132
        case "steam_cmd":
                return 27015
        default:
                return 25565
        }
}

// DeleteFlow stops + removes a server and (optionally) its files.
func (s *Service) DeleteFlow(serverID string, removeFiles bool, userID int64) error {
        return s.reg.Delete(serverID, removeFiles, userID)
}

// SteamCatalog exposes the catalog (API).
func (s *Service) SteamCatalog() []steamcmd.Game { return s.catalog.All() }

func b2i(b bool) int64 {
        if b {
                return 1
        }
        return 0
}
func ptr(s string) *string { return &s }
func orDefault(s, def string) string {
        if s != "" {
                return s
        }
        return def
}
func atoi(s string) int {
        v := 0
        for _, c := range s {
                if c < '0' || c > '9' {
                        return v
                }
                v = v*10 + int(c-'0')
        }
        return v
}