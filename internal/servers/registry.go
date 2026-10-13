package servers

import (
        "errors"
        "fmt"
        "os"
        "path/filepath"
        "strings"
        "sync"
        "time"

        "github.com/google/uuid"

        "github.com/FlameInTheDark/servy/internal/database"
        "github.com/FlameInTheDark/servy/internal/models"
        "github.com/FlameInTheDark/servy/internal/ports"
        "github.com/FlameInTheDark/servy/internal/repo/gen"
)

// ErrNotFound is returned when a server id does not exist.
var ErrNotFound = errors.New("SERVER_NOT_FOUND")

// Registry owns every local server supervisor and the CRUD surface.
type Registry struct {
        deps Deps
        mu   sync.RWMutex
        sups map[string]*Supervisor

        serversRoot string // absolute path of servers dir
        backupRoot  string
        autostartWG sync.WaitGroup
}

// NewRegistry builds the registry and wires every DB server.
func NewRegistry(deps Deps, serversRoot, backupRoot string) (*Registry, error) {
        r := &Registry{deps: deps, sups: map[string]*Supervisor{}, serversRoot: serversRoot, backupRoot: backupRoot}
        rows, err := deps.DB.ListServers()
        if err != nil {
                return nil, err
        }
        for _, def := range rows {
                if def.NodeID != nil && *def.NodeID != "" && *def.NodeID != LocalNodeID {
                        continue // remote-managed server; no local supervisor
                }
                r.sups[def.ServerID] = NewSupervisor(deps, def)
        }
        return r, nil
}

// Get returns the supervisor for a local server id.
func (r *Registry) Get(serverID string) (*Supervisor, error) {
        r.mu.RLock()
        defer r.mu.RUnlock()
        s, ok := r.sups[serverID]
        if !ok {
                // maybe remote node server: check DB
                if def, err := r.deps.DB.GetServer(serverID); err == nil && def.NodeID != nil && *def.NodeID != "" && *def.NodeID != LocalNodeID {
                        return nil, ErrRemoteServer
                }
                return nil, ErrNotFound
        }
        return s, nil
}

// ErrRemoteServer indicates the server lives on a cluster node.
var ErrRemoteServer = errors.New("REMOTE_SERVER")

// LocalNodeID is the sentinel for locally-managed servers.
const LocalNodeID = "local"

// All returns all local supervisors.
func (r *Registry) All() []*Supervisor {
        r.mu.RLock()
        defer r.mu.RUnlock()
        out := make([]*Supervisor, 0, len(r.sups))
        for _, s := range r.sups {
                out = append(out, s)
        }
        return out
}

// ServerPath returns the absolute path for a (new) server uuid.
func (r *Registry) ServerPath(id string) string { return filepath.Join(r.serversRoot, id) }

// BackupPath returns the backups dir for a server (by name, original layout).
func (r *Registry) BackupPath(serverName string) string {
        return filepath.Join(r.backupRoot, serverName)
}

// Create inserts a server definition and registers its supervisor.
// The server directory must already exist (creation wizard builds it).
func (r *Registry) Create(def gen.Server) (*Supervisor, error) {
        if def.ServerID == "" {
                def.ServerID = uuid.NewString()
        }
        if def.Created == "" {
                def.Created = database.Now()
        }
        def.LastUpdate = def.Created
        if _, err := os.Stat(def.Path); err != nil {
                return nil, fmt.Errorf("SERVER_DIR_MISSING: %s", def.Path)
        }
        if def.LogPath == "" {
                def.LogPath = "server.log"
        }
        row, err := r.deps.DB.CreateServer(def.ServerID, def.Created, def.LastUpdate, def.ServerName, def.Path,
                def.Executable, def.LogPath, def.ExecutionCommand, def.AutoStart, def.AutoStartDelay,
                def.CrashDetection, def.StopCommand, def.ExecutableUpdateUrl, def.ServerIp, def.ServerPort,
                def.LogsDeleteAfter, def.Type, def.ShowStatus, def.CreatedBy, def.ShutdownTimeout,
                def.IgnoredExits, def.AppID, def.CountPlayers, def.UpdateWatcher, def.StopAction, def.IoMode,
                def.NodeID, def.Definitions, def.JavaSelection, def.ExecutionBackend, def.DockerImage,
                def.DockerEnv, def.DockerPorts, def.DockerBinds, def.DockerNetwork, def.DockerWorkdir, def.DockerAutoremove,
                def.Shell)
        if err != nil {
                return nil, err
        }
        r.mu.Lock()
        r.sups[row.ServerID] = NewSupervisor(r.deps, row)
        r.mu.Unlock()
        // Reserve the host ports this server will publish. A conflict here is
        // reported instead of failing creation, so the user can remap and retry.
        r.reservePorts(row)
        return r.sups[row.ServerID], nil
}

// reservePorts claims the container-published host ports for a server so a
// second server cannot silently take the same port. Failures are logged, not
// fatal: the start path re-checks and surfaces a PortConflictError then.
func (r *Registry) reservePorts(def gen.Server) {
        if r.deps.Ports == nil || def.ExecutionBackend != "docker" {
                return
        }
        sup, err := r.Get(def.ServerID)
        if err != nil {
                return
        }
        specs := sup.buildDockerSpec(def, []string{"placeholder"})
        claims := make([]ports.Reservation, 0, len(specs.Ports))
        for _, spec := range specs.Ports {
                p, proto, ok := parsePortSpec(spec)
                if !ok {
                        continue
                }
                claims = append(claims, ports.Reservation{Port: p, Proto: proto})
        }
        if len(claims) == 0 {
                return
        }
        // Replace this server's previous claims so a port change releases the old one.
        r.deps.Ports.Release(def.ServerID)
        if err := r.deps.Ports.ClaimAll(def.ServerID, claims); err != nil {
                r.deps.Log.Get("server").Warning("server %s: port reservation %v", def.ServerName, err)
                _ = sup
        }
}

// UpdateConfig patches a server definition (full-column update).
func (r *Registry) UpdateConfig(serverID string, mutate func(*gen.Server)) error {
        sup, err := r.Get(serverID)
        if err != nil {
                return err
        }
        def, err := r.deps.DB.GetServer(serverID)
        if err != nil {
                return ErrNotFound
        }
        mutate(&def)
        def.LastUpdate = database.Now()
        // Port changes must re-reserve before persisting so the allocator never
        // hands the old port to another server. The allocator is optional
        // (headless agents / embedded tests may run without it).
        if r.deps.Ports != nil {
                r.deps.Ports.Release(serverID)
        }
        defer r.reservePorts(def)
        if err := r.deps.DB.UpdateServerConfig(def.LastUpdate, def.ServerName, def.Path, def.Executable,
                def.LogPath, def.ExecutionCommand, def.AutoStart, def.AutoStartDelay, def.CrashDetection,
                def.StopCommand, def.ExecutableUpdateUrl, def.ServerIp, def.ServerPort, def.LogsDeleteAfter,
                def.Type, def.ShowStatus, def.ShutdownTimeout, def.IgnoredExits, def.AppID, def.CountPlayers,
                def.UpdateWatcher, def.StopAction, def.IoMode, def.Definitions, def.JavaSelection,
                def.ExecutionBackend, def.DockerImage, def.DockerEnv, def.DockerPorts, def.DockerBinds,
                def.DockerNetwork, def.DockerWorkdir, def.DockerAutoremove, def.Shell, serverID); err != nil {
                return err
        }
        sup.mu.Lock()
        sup.def = def
        sup.mu.Unlock()
        return nil
}

// Delete removes a server: stops it (force), deletes rows, optionally files.
func (r *Registry) Delete(serverID string, removeFiles bool, userID int64) error {
        sup, err := r.Get(serverID)
        if err != nil {
                return err
        }
        _ = sup.Kill(userID)
        time.Sleep(300 * time.Millisecond)
        if err := r.deps.DB.DeleteServer(serverID); err != nil {
                return err
        }
        r.mu.Lock()
        delete(r.sups, serverID)
        r.mu.Unlock()
        // Free the host ports so another server can use them immediately.
        if r.deps.Ports != nil {
                r.deps.Ports.Release(serverID)
        }
        if removeFiles {
                if err := os.RemoveAll(sup.def.Path); err != nil {
                        r.deps.Log.Get("server").Error("failed to remove server files for %s: %v", serverID, err)
                        return fmt.Errorf("failed to remove server files: %w", err)
                }
        }
        // drop stats & schedules & backups & webhooks (FK cascade on server_id)
        r.deps.DB.DeleteServerStatsForServer(serverID)
        return nil
}

// AutostartAll boots servers flagged auto_start after their delay.
func (r *Registry) AutostartAll() {
        for _, sup := range r.All() {
                def := sup.Definition()
                if def.AutoStart != 1 {
                        continue
                }
                delay := time.Duration(def.AutoStartDelay) * time.Second
                s := sup
                r.autostartWG.Add(1)
                go func() {
                        defer r.autostartWG.Done()
                        if delay > 0 {
                                time.Sleep(delay)
                        }
                        if err := s.Start(0); err != nil {
                                r.deps.Log.Get("server").Warning("autostart of %s aborted: %v", def.ServerName, err)
                        }
                }()
        }
}

// StopAll gracefully stops every running server (shutdown hook).
func (r *Registry) StopAll() {
        var wg sync.WaitGroup
        for _, sup := range r.All() {
                if sup.State() == models.StateRunning {
                        wg.Add(1)
                        s := sup
                        go func() {
                                defer wg.Done()
                                _ = s.Stop("panel shutdown", 0)
                        }()
                }
        }
        wg.Wait()
}

// EULAPending reports whether the server needs EULA agreement before start.
func (r *Registry) EULAPending(serverID string) bool {
        sup, err := r.Get(serverID)
        if err != nil {
                return false
        }
        return !sup.eulaAccepted()
}

// AgreeEULA + optional start.
func (r *Registry) AgreeEULA(serverID string, userID int64) error {
        sup, err := r.Get(serverID)
        if err != nil {
                return err
        }
        if err := sup.AgreeEULA(); err != nil {
                return err
        }
        sup.SetRuntime(func(rt *models.ServerRuntime) { rt.EULAPending = false })
        r.deps.Log.Audit("", userID, serverID, "", "Agreed to EULA")
        return nil
}

// SanitizeServerName keeps names filesystem-safe for backup dir naming.
func SanitizeServerName(name string) string {
        name = strings.TrimSpace(name)
        out := strings.Map(func(r rune) rune {
                switch r {
                case '/', '\\', ':', '*', '?', '"', '<', '>', '|', ' ':
                        return '_'
                }
                return r
        }, name)
        if out == "" {
                out = "server"
        }
        return out
}
