// Package servers implements the server registry, process supervision,
// virtual terminals, stats collection, player tracking and update management.
package servers

import (
        "context"
        "errors"
        "fmt"
        "io"
        "os"
        "os/exec"
        "path/filepath"
        "strconv"
        "strings"
        "sync"
        "syscall"
        "time"

        "github.com/FlameInTheDark/servy/internal/config"
        "github.com/FlameInTheDark/servy/internal/database"
        "github.com/FlameInTheDark/servy/internal/defaults"
        "github.com/FlameInTheDark/servy/internal/dockerx"
        "github.com/FlameInTheDark/servy/internal/events"
        "github.com/FlameInTheDark/servy/internal/logging"
        "github.com/FlameInTheDark/servy/internal/models"
        "github.com/FlameInTheDark/servy/internal/ports"
        "github.com/FlameInTheDark/servy/internal/repo/gen"
        "github.com/FlameInTheDark/servy/internal/steamcmd"
)

// Error taxonomy for process operations.
var (
        ErrEULA             = errors.New("EULA_AGREEMENT_REQUIRED")
        ErrServerRunning    = errors.New("SERVER_ALREADY_RUNNING")
        ErrServerNotRunning = errors.New("SERVER_NOT_RUNNING")
        ErrServerDisabled   = errors.New("SERVER_DISABLED")
        ErrServerImporting  = errors.New("SERVER_IMPORTING")
        ErrServerUpdating   = errors.New("SERVER_UPDATING")
        ErrBackupRunning    = errors.New("BACKUP_IN_PROGRESS")
)

// Deps bundles supervisor/registry dependencies.
type Deps struct {
        DB      *database.Queries
        Bus     *events.Bus
        Log     *logging.Manager
        Cfg     *config.Manager
        Docker  *dockerx.Manager    // nil = docker execution unavailable on this host
        Catalog *steamcmd.Catalog   // steam game catalog (query ports, save dirs); may be nil
        Ports   *ports.Allocator    // host port reservations for containerized servers; may be nil
}

// Supervisor owns one server's child process and per-run goroutines.
type Supervisor struct {
        deps Deps
        def  gen.Server // DB row snapshot

        mu         sync.RWMutex
        state      models.ServerState
        cmd        *exec.Cmd
        stdinW     *os.File
        stdinPipe  interface{ Write([]byte) (int, error) }
        tty        *os.File
        terminal   *Terminal
        runtime    models.ServerRuntime
        crashCount int
        stopping   bool      // intentional stop in flight (suppress crash path)
        shutdownAt time.Time // deadline for force kill
        waitCh     chan int  // process exit code
        dead       bool
        // docker execution backend state (dockerID != "" while a container runs)
        dockerID        string
        dockerStdin     io.WriteCloser
        dockerCancel    func()
        lastDockerStats *dockerx.ContainerStats // previous one-shot sample (CPU delta)
        lastDockerAt    time.Time
        // command queue (flushed on Running)
        queue []string
        // callbacks into registry
        onExit    func(sup *Supervisor)
        collector *Collector
}

// NewSupervisor creates a supervisor for a server definition.
func NewSupervisor(deps Deps, def gen.Server) *Supervisor {
        cfg := deps.Cfg.Get()
        term := NewTerminal(cfg.VirtualTerminalLines, cfg.Keywords)
        sup := &Supervisor{
                deps:     deps,
                def:      def,
                state:    models.StateStopped,
                terminal: term,
        }
        term.SetPublisher(func(e events.Event) {
                e.ServerID = def.ServerID
                deps.Bus.Publish(e)
        })
        return sup
}

// Terminal exposes the virtual terminal buffer.
func (s *Supervisor) Terminal() *Terminal { return s.terminal }

// Definition returns the DB row snapshot.
func (s *Supervisor) Definition() gen.Server { return s.def }

// ReloadDefinition refreshes the DB snapshot (config edit).
func (s *Supervisor) ReloadDefinition() error {
        def, err := s.deps.DB.GetServer(s.def.ServerID)
        if err != nil {
                return err
        }
        s.def = def
        return nil
}

// State returns the current lifecycle state.
func (s *Supervisor) State() models.ServerState {
        s.mu.RLock()
        defer s.mu.RUnlock()
        return s.state
}

// Runtime returns a copy of the live runtime snapshot.
func (s *Supervisor) Runtime() models.ServerRuntime {
        s.mu.RLock()
        defer s.mu.RUnlock()
        r := s.runtime
        r.ServerID = s.def.ServerID
        st := s.state
        r.State = st
        r.Running = st == models.StateRunning || st == models.StateStarting || st == models.StateStopping
        return r
}

// setState updates state and broadcasts send_start_reload (list refresh).
func (s *Supervisor) setState(st models.ServerState) {
        s.mu.Lock()
        s.state = st
        if st == models.StateRunning {
                s.runtime.Crashed = false
        }
        s.mu.Unlock()
        s.deps.Bus.Publish(events.Event{Type: events.EvSendStartReload, ServerID: s.def.ServerID,
                Data: map[string]interface{}{"server_id": s.def.ServerID, "state": string(st)}})
}

// ---------------- EULA ----------------

// eulaAccepted checks (minecraft-java only) whether eula.txt contains eula=true.
func (s *Supervisor) eulaAccepted() bool {
        if s.def.Type != models.TypeMinecraftJava {
                return true
        }
        data, err := os.ReadFile(filepath.Join(s.def.Path, "eula.txt"))
        if err != nil {
                return false
        }
        for _, line := range strings.Split(string(data), "\n") {
                l := strings.ToLower(strings.TrimSpace(line))
                if l == "eula=true" {
                        return true
                }
        }
        return false
}

// AgreeEULA writes eula=true into eula.txt (minecraft-java only).
func (s *Supervisor) AgreeEULA() error {
        if s.def.Type != models.TypeMinecraftJava {
                return nil
        }
        eula := "eula=true\n"
        return os.WriteFile(filepath.Join(s.def.Path, "eula.txt"), []byte(eula), 0o644)
}

// ---------------- Start ----------------

// executionArgv resolves manifest placeholders and appends typed Steam launch
// values without flattening user input into a shell command string.
func (s *Supervisor) executionArgv(def gen.Server) ([]string, error) {
	if strings.ContainsRune(def.ExecutionCommand, '\x00') {
		return nil, errors.New("execution command contains a NUL byte")
	}
	argv := splitCommand(def.ExecutionCommand)
	if len(argv) == 0 {
		return nil, errors.New("EMPTY_EXECUTION_COMMAND")
	}
	if def.Type != models.TypeSteamCmd {
		return argv, nil
	}
	snapshot, err := steamcmd.GameSnapshotFromDefinitions(def.Definitions)
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return argv, nil
	}
	port := int(def.ServerPort)
	queryPort := snapshot.Game.QueryPort
	if queryPort == 0 {
		queryPort = port + 1
	}
	saveDir := filepath.Join(def.Path, "saves")
	if strings.ContainsAny(def.ServerName+saveDir, "\x00\r\n") {
		return nil, errors.New("server name or save directory contains a control character")
	}
	context := steamcmd.LaunchContext{
		ServerName: def.ServerName, Port: port, QueryPort: queryPort, SaveDir: saveDir,
	}
	replacer := strings.NewReplacer(
		"{SERVER_NAME}", def.ServerName,
		"{PORT}", strconv.Itoa(port),
		"{QUERY_PORT}", strconv.Itoa(queryPort),
		"{SAVE_DIR}", saveDir,
	)
	for i := range argv {
		argv[i] = replacer.Replace(argv[i])
	}
	launchArgs, _, err := steamcmd.ResolveLaunchOptions(snapshot.Game.LaunchOptions, snapshot.LaunchOptions, context)
	if err != nil {
		return nil, err
	}
	return append(argv, launchArgs...), nil
}

// gameManifest returns the server's frozen catalog entry when available. Older
// server rows predate snapshots, so their legacy behavior falls back to catalog.
func (s *Supervisor) gameManifest(def gen.Server) (steamcmd.Game, bool) {
	if snapshot, err := steamcmd.GameSnapshotFromDefinitions(def.Definitions); err == nil && snapshot != nil {
		return snapshot.Game, true
	}
	if s.deps.Catalog != nil && def.AppID != nil && *def.AppID != "" {
		return s.deps.Catalog.ByAppID(*def.AppID)
	}
	return steamcmd.Game{}, false
}

// Start launches the server process. userID/lang drive user-facing notifications.
func (s *Supervisor) Start(userID int64) error {
        s.mu.Lock()
        if s.state == models.StateRunning || s.state == models.StateStarting {
                s.mu.Unlock()
                return ErrServerRunning
        }
        if s.state == models.StateUpdating {
                s.mu.Unlock()
                return ErrServerUpdating
        }
        if s.state == models.StateImporting {
                s.mu.Unlock()
                return ErrServerImporting
        }
        s.mu.Unlock()

        // reload latest definition
        if err := s.ReloadDefinition(); err != nil {
                return err
        }
        def := s.def

        // EULA gate
        if !s.eulaAccepted() {
                s.mu.Lock()
                s.runtime.EULAPending = true
                s.mu.Unlock()
                s.deps.Bus.Publish(events.Event{Type: events.EvSendEulaBootbox, ServerID: def.ServerID,
                        Data: map[string]interface{}{"server_id": def.ServerID, "user_id": userID}})
                s.deps.Log.Get("server").Warning("server %s (%s) refused start: EULA not accepted", def.ServerName, def.ServerID)
                return ErrEULA
        }

        // path sanity (process backend: the executable must exist on this
        // host; docker backend: it may live inside the image)
        if def.ExecutionBackend != "docker" {
                exePath := filepath.Join(def.Path, def.Executable)
                if _, err := os.Stat(exePath); err != nil {
                        msg := fmt.Sprintf("executable %s missing (%v)", exePath, err)
                        s.deps.Log.Get("server").Error("server %s: %s", def.ServerName, msg)
                        return fmt.Errorf("EXECUTABLE_MISSING: %s", msg)
                }
        }

        s.setState(models.StateStarting)
        s.terminal.Clear()

        argv, err := s.executionArgv(def)
        if err != nil {
                s.setState(models.StateStopped)
                return err
        }
        if def.ExecutionBackend == "docker" {
                return s.startDocker(def, argv, userID)
        }
        // per-server java selection (legacy get_execution_java semantics)
        argv = ApplyJavaSelection(argv, def.JavaSelection)
        // Frozen Steam game manifests are argv templates rather than shell
        // scripts; always exec them directly so launch values cannot be
        // expanded or reinterpreted by a shell.
        shellSelection := def.Shell
        if def.Type == models.TypeSteamCmd {
                if snapshot, snapshotErr := steamcmd.GameSnapshotFromDefinitions(def.Definitions); snapshotErr == nil && snapshot != nil {
                        shellSelection = "none"
                }
        }
        // Other server types may use their selected shell to launch scripts.
        exe, args, err := BuildShellCommand(shellSelection, argv)
        if err != nil {
                s.setState(models.StateStopped)
                s.deps.Log.Get("server").Error("server %s: terminal %q unavailable on this host", def.ServerName, def.Shell)
                return fmt.Errorf("%w: %q", err, def.Shell)
        }
        if spec, _ := ResolveShell(shellSelection); spec.Kind != "none" && shellSelection != "" && shellSelection != "auto" {
                s.deps.Log.Get("server").Info("server %s: launching via %s", def.ServerName, spec.Label)
        }
        cmd := exec.Command(exe, args...)
        cmd.Dir = def.Path
        cmd.Env = s.buildEnv()

        usePTY := def.IoMode == "pty" && ptyAvailable()
        if usePTY {
                tty, err := ptyStart(cmd)
                if err != nil {
                        s.setState(models.StateStopped)
                        s.deps.Log.Get("server").Error("server %s PTY start failed: %v (falling back to pipes)", def.ServerName, err)
                        return s.Start(userID)
                }
                s.mu.Lock()
                s.tty = tty
                s.stdinW = tty
                s.mu.Unlock()
                go s.readLoop(tty)
        } else {
                pr, pw, err := os.Pipe()
                if err != nil {
                        s.setState(models.StateStopped)
                        return err
                }
                cmd.Stdout = pw
                cmd.Stderr = pw
                stdinPipe, err := cmd.StdinPipe()
                if err != nil {
                        pw.Close()
                        s.setState(models.StateStopped)
                        return err
                }
                setupProcessGroup(cmd)
                if err := cmd.Start(); err != nil {
                        pw.Close()
                        pr.Close()
                        s.setState(models.StateStopped)
                        s.deps.Log.Get("server").Error("server %s failed to start: %v", def.ServerName, err)
                        return fmt.Errorf("START_FAILED: %v", err)
                }
                pw.Close()
                s.mu.Lock()
                s.stdinW = nil
                s.mu.Unlock()
                go func() {
                        s.writeLoop(pr, stdinPipe)
                }()
        }

        s.mu.Lock()
        s.cmd = cmd
        s.dead = false
        s.stopping = false
        s.waitCh = make(chan int, 1)
        now := time.Now().UTC()
        s.runtime = models.ServerRuntime{
                ServerID:  def.ServerID,
                State:     models.StateRunning,
                StartedAt: now,
                Players:   []string{},
        }
        // capture PID while exclusively locked: handleExit() may call
        // Process.Release() the instant the child dies, and Release()
        // sets Process.Pid = -1 (racing unlocked readers, e.g. the log line
        // at the end of Start).
        pid := cmd.Process.Pid
        s.mu.Unlock()

        // first-run marker (.servy) — port reminder notice
        marker := filepath.Join(def.Path, ".servy")
        if _, err := os.Stat(marker); err != nil {
                s.deps.Bus.Publish(events.Event{Type: events.EvNotification, ServerID: def.ServerID,
                        Data: map[string]interface{}{"title": "Server started", "message": fmt.Sprintf(
                                "'%s' is running for the first time. Check the server port (%d) is open on your router if friends cannot connect.",
                                def.ServerName, derivePort(def, nil)), "user_id": userID}})
                os.WriteFile(marker, []byte(now.Format(time.RFC3339)), 0o644)
        }

        // wait for exit
        go func() {
                err := cmd.Wait()
                code := 0
                if err != nil {
                        if ee, ok := err.(*exec.ExitError); ok {
                                code = ee.ExitCode()
                        } else {
                                code = -1
                        }
                }
                select {
                case s.waitCh <- code:
                default:
                }
                s.handleExit(code, err)
        }()

        s.setState(models.StateRunning)
        s.StartCollector()

        // flush queued commands
        s.mu.Lock()
        q := s.queue
        s.queue = nil
        s.mu.Unlock()
        for _, c := range q {
                s.writeStdin(c)
        }

        s.deps.Log.Get("server").Info("server %s (%s) started with PID %d (io=%s)", def.ServerName, def.ServerID, pid, def.IoMode)
        s.deps.Log.Audit("", userID, def.ServerID, "", fmt.Sprintf("Started server %s", def.ServerName))
        s.deps.Bus.Publish(events.Event{Type: events.EvServerStart, ServerID: def.ServerID,
                Data: map[string]interface{}{"server_id": def.ServerID, "name": def.ServerName}})
        return nil
}

// buildEnv assembles the child environment (launch strategies per type).
func (s *Supervisor) buildEnv() []string {
        env := os.Environ()
        switch s.def.Type {
        case models.TypeMinecraftBedrock:
                if !isWindows() {
                        env = append(env, "LD_LIBRARY_PATH="+s.def.Path)
                }
        case models.TypeSteamCmd:
                env = applyEnvJSON(s.def.Path, s.def.Path, env)
        }
        return env
}

// applyEnvJSON implements legacy env.json injection (steam servers):
// {"KEY": {"contents": [...], "mode": "append|prepend|replace"}}; keys
// containing "path" join with ":" and are traversal-validated, others ",".
//
// serverPath is where env.json lives (the server dir on this host);
// resolveBase is the base relative path items resolve against. The two
// differ for the docker backend: the file sits in the server dir on the
// panel host (def.Path) but relative entries like "./linux64" must become
// in-container paths (workdir, e.g. /data/linux64) — resolving them against
// the panel path would leak a panel-side absolute path into the container.
func applyEnvJSON(serverPath, resolveBase string, env []string) []string {
        data, err := os.ReadFile(filepath.Join(serverPath, "env.json"))
        if err != nil {
                return env
        }
        // never mutate the caller's backing array (both backends pass their
        // live env slice; in-place writes would leak edits between calls)
        env = append([]string(nil), env...)
        type entry struct {
                Contents []string `json:"contents"`
                Mode     string   `json:"mode"`
        }
        var spec map[string]entry
        if err := jsonUnmarshal(data, &spec); err != nil {
                return env
        }
        for key, e := range spec {
                isPath := strings.Contains(strings.ToLower(key), "path")
                items := make([]string, 0, len(e.Contents))
                for _, item := range e.Contents {
                        if isPath {
                                p, err := validateWithin(resolveBase, item)
                                if err != nil {
                                        continue // traversal — skip
                                }
                                items = append(items, strings.ReplaceAll(p, ":", "\\:"))
                        } else {
                                items = append(items, item)
                        }
                }
                sep := ":"
                if !isPath {
                        sep = ","
                }
                joined := strings.Join(items, sep)
                for i, kv := range env {
                        if strings.HasPrefix(kv, key+"=") {
                                existing := strings.TrimPrefix(kv, key+"=")
                                switch e.Mode {
                                case "append":
                                        joined = existing + sep + joined
                                case "prepend":
                                        joined = joined + sep + existing
                                }
                                env[i] = key + "=" + joined
                                goto next
                        }
                }
                env = append(env, key+"="+joined)
        next:
        }
        return env
}

// validateWithin ensures p (possibly relative) resolves inside base.
func validateWithin(base, p string) (string, error) {
        abs, err := filepath.Abs(filepath.Join(base, p))
        if err != nil {
                return "", err
        }
        root, err := filepath.Abs(base)
        if err != nil {
                return "", err
        }
        if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
                return "", errors.New("TRAVERSAL DETECTED")
        }
        return abs, nil
}

// readLoop consumes PTY output into the terminal buffer.
func (s *Supervisor) readLoop(f *os.File) {
        buf := make([]byte, 4096)
        for {
                n, err := f.Read(buf)
                if n > 0 {
                        s.terminal.Add(buf[:n])
                }
                if err != nil {
                        return
                }
        }
}

// writeLoop copies pipe output into the terminal and forwards stdin.
func (s *Supervisor) writeLoop(pr *os.File, stdin interface{ Write([]byte) (int, error) }) {
        go func() {
                buf := make([]byte, 4096)
                for {
                        n, err := pr.Read(buf)
                        if n > 0 {
                                s.terminal.Add(buf[:n])
                        }
                        if err != nil {
                                return
                        }
                }
        }()
        s.mu.Lock()
        s.stdinPipe = stdin
        s.mu.Unlock()
}

// ---------------- stdin / commands ----------------

// writeStdin sends a raw line to the process stdin (no queueing).
func (s *Supervisor) writeStdin(line string) error {
        s.mu.Lock()
        w := s.stdinW
        pipe := s.stdinPipe
        running := s.state == models.StateRunning && !s.dead
        s.mu.Unlock()
        if !running {
                return ErrServerNotRunning
        }
        if w != nil {
                _, err := w.WriteString(line + "\n")
                return err
        }
        if pipe != nil {
                _, err := pipe.Write([]byte(line + "\n"))
                return err
        }
        s.mu.RLock()
        dw := s.dockerStdin
        s.mu.RUnlock()
        if dw != nil {
                _, err := dw.Write([]byte(line + "\n"))
                return err
        }
        return ErrServerNotRunning
}

// SendCommand queues/executes a console command. "start" is a pseudo-command.
func (s *Supervisor) SendCommand(command string, userID int64) error {
        if command == "start" {
                return s.Start(userID)
        }
        st := s.State()
        if st == models.StateStarting {
                s.mu.Lock()
                s.queue = append(s.queue, command)
                s.mu.Unlock()
                return nil
        }
        if st != models.StateRunning {
                return ErrServerNotRunning
        }
        if err := s.writeStdin(command); err != nil {
                return err
        }
        s.deps.Log.Audit("", userID, s.def.ServerID, "", fmt.Sprintf("Sent command '%s' to server %s", command, s.def.ServerName))
        s.deps.Bus.Publish(events.Event{Type: events.EvSendCommand, ServerID: s.def.ServerID,
                Data: map[string]interface{}{"server_id": s.def.ServerID, "command": command, "name": s.def.ServerName}})
        return nil
}

// ---------------- Stop / Kill / Restart ----------------

// Stop gracefully stops the server: stop command via stdin, 2s poll loop,
// force kill at shutdown_timeout. Returns when the process is gone.
func (s *Supervisor) Stop(reason string, userID int64) error {
        s.mu.Lock()
        if s.state != models.StateRunning && s.state != models.StateStarting && s.state != models.StateCrashed {
                s.mu.Unlock()
                return ErrServerNotRunning
        }
        s.stopping = true
        timeout := time.Duration(s.def.ShutdownTimeout) * time.Second
        if timeout <= 0 {
                timeout = 60 * time.Second
        }
        if s.isDocker() {
                s.mu.Unlock()
                return s.stopDocker(reason, userID, timeout)
        }
        var cmd *exec.Cmd
        var signalStop bool
        if s.cmd != nil {
                cmd = s.cmd
        }
        stdinLine := s.def.StopCommand
        action := s.def.StopAction
        // signal under the lock: handleExit() calls Process.Release() under
        // the same mutex and Release() rewrites Process.Pid — signalGroup
        // must not race it (fast syscalls only, holding the lock is safe).
        if cmd != nil && cmd.Process != nil && (action == "signal" || stdinLine == "") {
                signalGroup(cmd, syscall.SIGTERM)
                signalStop = true
        }
        s.mu.Unlock()

        // deliver the stop command while still in Running state (writeStdin gates
        // on Running), then transition to Stopping
        if signalStop {
                s.setState(models.StateStopping)
        } else if cmd != nil && cmd.Process != nil {
                _ = s.writeStdin(stdinLine)
                s.setState(models.StateStopping)
        } else {
                s.setState(models.StateStopping)
        }
        s.deps.Log.Get("server").Info("stopping server %s (%s)", s.def.ServerName, reason)

        deadline := time.Now().Add(timeout)
        tick := time.NewTicker(500 * time.Millisecond)
        defer tick.Stop()
        for {
                s.mu.RLock()
                dead := s.dead
                s.mu.RUnlock()
                if dead {
                        break
                }
                if time.Now().After(deadline) {
                        s.deps.Log.Get("server").Warning("server %s stop timeout reached — killing", s.def.ServerName)
                        s.Kill(userID)
                        break
                }
                <-tick.C
        }
        s.deps.Log.Audit("", userID, s.def.ServerID, "", fmt.Sprintf("Stopped server %s (%s)", s.def.ServerName, reason))
        s.deps.Bus.Publish(events.Event{Type: events.EvServerStop, ServerID: s.def.ServerID,
                Data: map[string]interface{}{"server_id": s.def.ServerID, "name": s.def.ServerName}})
        return nil
}

// Kill force-terminates the whole process tree (or the container).
func (s *Supervisor) Kill(userID int64) error {
        if s.isDocker() {
                s.mu.Lock()
                id := s.dockerID
                s.stopping = true
                s.mu.Unlock()
                if id == "" || s.deps.Docker == nil {
                        return ErrServerNotRunning
                }
                kctx, kcancel := context.WithTimeout(context.Background(), 10*time.Second)
                defer kcancel()
                if err := s.deps.Docker.Kill(kctx, id); err != nil {
                        return err
                }
                s.deps.Log.Audit("", userID, s.def.ServerID, "", fmt.Sprintf("Killed server %s (container)", s.def.ServerName))
                s.deps.Bus.Publish(events.Event{Type: events.EvServerKill, ServerID: s.def.ServerID,
                        Data: map[string]interface{}{"server_id": s.def.ServerID, "name": s.def.ServerName}})
                return nil
        }
        s.mu.Lock()
        cmd := s.cmd
        s.stopping = true
        // kill under the lock: handleExit() calls Process.Release() under the
        // same mutex and Release() rewrites Process.Pid to -1 — a racing
        // killGroup could otherwise target the wrong process group.
        if cmd != nil && cmd.Process != nil {
                killGroup(cmd)
        }
        s.mu.Unlock()
        if cmd == nil || cmd.Process == nil {
                return ErrServerNotRunning
        }
        s.deps.Log.Audit("", userID, s.def.ServerID, "", fmt.Sprintf("Killed server %s", s.def.ServerName))
        s.deps.Bus.Publish(events.Event{Type: events.EvServerKill, ServerID: s.def.ServerID,
                Data: map[string]interface{}{"server_id": s.def.ServerID, "name": s.def.ServerName}})
        return nil
}

// Restart = stop → 2s grace → start (suppressed during backup-shutdown).
func (s *Supervisor) Restart(userID int64, backupLock *BackupLock) error {
        if backupLock != nil && backupLock.Held() {
                return ErrBackupRunning
        }
        wasRunning := s.State() == models.StateRunning
        if wasRunning {
                if err := s.Stop("restart", userID); err != nil && err != ErrServerNotRunning {
                        return err
                }
                time.Sleep(2 * time.Second)
        }
        s.mu.Lock()
        s.crashCount = 0
        s.mu.Unlock()
        return s.Start(userID)
}

// ---------------- docker execution backend ----------------

// isDocker reports whether this server runs via the docker backend.
func (s *Supervisor) isDocker() bool {
        return s.def.ExecutionBackend == "docker"
}

// pingTarget resolves the address the status query (A2S / RakNet / Java ping)
// should be sent to. Panel-created servers pin def.ServerIp to 127.0.0.1,
// which only reaches docker-backend servers when servy itself runs on the
// host (via the published ports). Prefer the container's own IP on the docker
// bridge — reachable from a host-networked panel AND from a containerized
// panel sharing the network — and fall back to the stored address when the
// container is not running (or docker is unavailable).
func (s *Supervisor) pingTarget() string {
        ip := s.def.ServerIp
        if !s.isDocker() || s.deps.Docker == nil {
                return ip
        }
        s.mu.RLock()
        id := s.dockerID
        s.mu.RUnlock()
        if id == "" {
                return ip
        }
        ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
        defer cancel()
        ips, err := s.deps.Docker.ContainerIPs(ctx, id)
        if err != nil || len(ips) == 0 {
                return ip
        }
        return ips[0]
}

// catalogPorts resolves the host ports a containerized server must publish
// from the game catalog's `expose` list, resolved against the configured game
// port. Explicit per-server `docker_ports` always win (checked by the caller).
// Entries with the same port+proto are de-duplicated.
func (s *Supervisor) catalogPorts(def gen.Server) []string {
        game, ok := s.gameManifest(def)
        if !ok || len(game.Expose) == 0 {
                return nil
        }
        base := int(def.ServerPort)
        seen := map[string]bool{}
        var out []string
        for _, e := range game.Expose {
                p := e.Resolve(base)
                if p <= 0 || p > 65535 {
                        continue
                }
                proto := e.ProtoOrDefault()
                spec := fmt.Sprintf("%d:%d/%s", p, p, proto)
                if seen[spec] {
                        continue
                }
                seen[spec] = true
                out = append(out, spec)
        }
        return out
}

// buildDockerSpec converts the server definition into a container run spec.
// The server directory is bind-mounted to the working dir (default /data);
// the game port is published unless explicit port specs are configured.
//
// When servy itself runs inside a container the server directory path only
// exists in servy's own mount namespace — the bind is re-rooted onto the host
// path (SERVY_HOST_DATA_PATH) so the Docker daemon can actually mount it.
func (s *Supervisor) buildDockerSpec(def gen.Server, argv []string) dockerx.RunSpec {
        workdir := "/data"
        if def.DockerWorkdir != nil && *def.DockerWorkdir != "" {
                workdir = *def.DockerWorkdir
        }
        binds := []string{}
        if hostPath, err := dockerx.HostBindPath(def.Path); err == nil {
                binds = append(binds, hostPath+":"+workdir)
        } else {
                s.deps.Log.Get("server").Error("server %s: %v — falling back to the untranslated path (sibling container will likely see an empty directory)", def.ServerName, err)
                binds = append(binds, def.Path+":"+workdir)
        }
        for _, b := range parseJSONStrings(def.DockerBinds) {
                if b == "" {
                        continue
                }
                // translate the HOST side of user binds that live under the
                // servy data root; other paths pass through unchanged
                parts := strings.SplitN(b, ":", 2)
                if hostPath, err := dockerx.HostBindPath(parts[0]); err == nil {
                        b = hostPath
                        if len(parts) == 2 {
                                b += ":" + parts[1]
                        }
                }
                binds = append(binds, b)
        }
        ports := parseJSONStrings(def.DockerPorts)
        if len(ports) == 0 {
                ports = s.catalogPorts(def)
        }
        if len(ports) == 0 {
                // publish the game port by default (tcp; bedrock adds udp too)
                p := int(def.ServerPort)
                ports = []string{fmt.Sprintf("%d:%d/tcp", p, p)}
                if def.Type == models.TypeMinecraftBedrock {
                        ports = append(ports, fmt.Sprintf("%d:%d/udp", p, p))
                }
        }
        env := []string{"TERM=xterm"}
        user := ""
        for k, v := range parseJSONMap(def.DockerEnv) {
                // escape hatch: SERVY_DOCKER_USER overrides the container user
                // ("image" or "" -> keep the image default; "uid[:gid]" -> that user)
                if k == "SERVY_DOCKER_USER" {
                        if v != "image" {
                                user = v
                        }
                        continue // never leaked into the container environment
                }
                env = append(env, k+"="+v)
        }
        if user == "" {
                // default: run the container with servy's own uid:gid so it can
                // write the bind-mounted server dir (image default users often
                // cannot). Skipped on non-Linux (Docker Desktop maps for us).
                user = dockerx.CurrentUserSpec()
        }
        // legacy env.json injection (steam servers): the file lives in the
        // server dir on THIS host (def.Path); relative path entries resolve
        // against the in-container workdir so e.g. valheim's ./linux64 becomes
        // /data/linux64 instead of a panel-side path the container can't see.
        // Without it the game cannot dlopen steamclient.so and dies right
        // after boot with "GameServer.Init() failed" (valheim, rust, ...).
        if def.Type == models.TypeSteamCmd {
                env = applyEnvJSON(def.Path, workdir, env)
        }
        network := ""
        if def.DockerNetwork != nil {
                network = *def.DockerNetwork
        }
        image := ""
        if def.DockerImage != nil {
                image = *def.DockerImage
        }
        // The creation wizard bakes panel-side absolute paths into the
        // execution command ({SAVE_DIR} → <server dir>/saves). Inside the
        // sibling container the server dir is mounted at the workdir, so
        // rewrite that prefix — otherwise the game process would look for
        // saves under a path that only exists in the panel's mount namespace.
        if def.Path != "" && def.Path != workdir {
                for i, a := range argv {
                        switch {
                        case a == def.Path:
                                argv[i] = workdir
                        case strings.HasPrefix(a, def.Path+"/"):
                                argv[i] = workdir + a[len(def.Path):]
                        }
                }
        }
        return dockerx.RunSpec{
                Name:       "servy-" + def.ServerID,
                Image:      image,
                Cmd:        argv,
                Env:        env,
                User:       user,
                WorkingDir: workdir,
                Binds:      binds,
                Ports:      ports,
                Network:    network,
                AutoRemove: def.DockerAutoremove == 1,
                Labels: map[string]string{
                        "servy.managed": "true", "servy.server_id": def.ServerID,
                },
        }
}

// parsePortSpec splits a "host:container/proto" (or "port/proto") docker spec.
func parsePortSpec(spec string) (host int, proto string, ok bool) {
        proto = "tcp"
        body := spec
        if i := indexByte(spec, '/'); i >= 0 {
                proto = spec[i+1:]
                body = spec[:i]
        }
        hostPart := body
        if i := lastIndexByte(body, ':'); i >= 0 {
                hostPart = body[:i]
        }
        host = atoiSafe(hostPart)
        if host <= 0 {
                return 0, proto, false
        }
        return host, proto, true
}

func indexByte(s string, b byte) int {
        for i := 0; i < len(s); i++ {
                if s[i] == b {
                        return i
                }
        }
        return -1
}

func lastIndexByte(s string, b byte) int {
        for i := len(s) - 1; i >= 0; i-- {
                if s[i] == b {
                        return i
                }
        }
        return -1
}

// filterUsablePorts drops optional ports that are already taken and returns a
// PortConflictError when a required port collides with another managed server
// or an unrelated process on this host.
func (s *Supervisor) filterUsablePorts(def gen.Server, specs []string) ([]string, error) {
        if len(specs) == 0 || s.deps.Ports == nil {
                return specs, nil
        }
        // Which ports are optional per the catalog?
        optional := map[int]bool{}
        if game, ok := s.gameManifest(def); ok {
                        base := int(def.ServerPort)
                        for _, e := range game.Expose {
                                if e.Optional {
                                        optional[e.Resolve(base)] = true
                                }
                        }
                }

        var (
                kept  []string
                taken []string
        )
        for _, spec := range specs {
                port, proto, ok := parsePortSpec(spec)
                if !ok {
                        kept = append(kept, spec)
                        continue
                }
                if !s.deps.Ports.Available(def.ServerID, port, proto) {
                        if optional[port] {
                                s.deps.Log.Get("server").Info("server %s: optional port %d/%s is in use — starting without it", def.ServerName, port, proto)
                                continue
                        }
                        taken = append(taken, fmt.Sprintf("%d/%s", port, proto))
                        continue
                }
                kept = append(kept, spec)
        }
        if len(taken) > 0 {
                return nil, &PortConflictError{ServerID: def.ServerID, Ports: taken}
        }
        return kept, nil
}

// PortConflictError reports required host ports that could not be published.
type PortConflictError struct {
        ServerID string   `json:"server_id"`
        Ports    []string `json:"ports"` // "port/proto"
}

func (e *PortConflictError) Error() string {
        return "PORT_CONFLICT: host port already in use: " + strings.Join(e.Ports, ", ")
}

// startDocker launches the server inside a Docker container: pull the image,
// create + start the container, attach its stdio to the virtual terminal and
// wait for its exit code exactly like a child process.
func (s *Supervisor) startDocker(def gen.Server, argv []string, userID int64) error {
        if s.deps.Docker == nil || !s.deps.Docker.Available() {
                s.setState(models.StateStopped)
                s.deps.Log.Get("server").Error("server %s: docker backend selected but the Docker daemon is unreachable (mount /var/run/docker.sock?)", def.ServerName)
                return fmt.Errorf("DOCKER_UNAVAILABLE")
        }
        spec := s.buildDockerSpec(def, argv)
        if spec.Image == "" {
                // steam servers without an explicit image (created before image
                // defaulting existed, or an empty override) still start out of the box
                if def.AppID != nil && *def.AppID != "" {
                        spec.Image = defaults.DockerSteamWineImage
                        s.deps.Log.Get("server").Info("server %s: no image configured — using the default %s", def.ServerName, spec.Image)
                } else {
                        s.setState(models.StateStopped)
                        return errors.New("DOCKER_IMAGE_REQUIRED")
                }
        }
        // Refuse to start when a required host port is already taken: Docker
        // would fail with an opaque "port is already allocated", and the
        // container would restart-loop. Optional ports (query ports some game
        // modes never open) are dropped instead of blocking the start.
        ports, err := s.filterUsablePorts(def, spec.Ports)
        if err != nil {
                s.setState(models.StateStopped)
                s.deps.Log.Get("server").Error("server %s: %v", def.ServerName, err)
                return err
        }
        spec.Ports = ports
        pullCtx, pullCancel := context.WithTimeout(context.Background(), 15*time.Minute)
        defer pullCancel()
        if err := s.deps.Docker.EnsureImage(pullCtx, spec.Image, func(line string) {
                s.terminal.Add([]byte("\r\n[docker] " + line + "\r\n"))
        }); err != nil {
                s.setState(models.StateStopped)
                s.deps.Log.Get("server").Error("server %s: image %s unavailable: %v", def.ServerName, spec.Image, err)
                return fmt.Errorf("IMAGE_PULL_FAILED: %v", err)
        }
        ctx := context.Background()
        id, err := s.deps.Docker.Run(ctx, spec)
        if err != nil {
                s.setState(models.StateStopped)
                s.deps.Log.Get("server").Error("server %s: container start failed: %v", def.ServerName, err)
                return fmt.Errorf("DOCKER_START_FAILED: %v", err)
        }
        stdout, stdin, cancel, err := s.deps.Docker.Attach(ctx, id)
        if err != nil {
                _ = s.deps.Docker.Kill(ctx, id)
                s.setState(models.StateStopped)
                return fmt.Errorf("DOCKER_ATTACH_FAILED: %v", err)
        }
        // stream container output into the virtual terminal
        go func() {
                buf := make([]byte, 4096)
                for {
                        n, rerr := stdout.Read(buf)
                        if n > 0 {
                                s.terminal.Add(buf[:n])
                        }
                        if rerr != nil {
                                return
                        }
                }
        }()

        s.mu.Lock()
        s.dockerID = id
        s.dockerStdin = stdin
        s.dockerCancel = cancel
        s.dead = false
        s.stopping = false
        s.waitCh = make(chan int, 1)
        now := time.Now().UTC()
        s.runtime = models.ServerRuntime{
                ServerID: def.ServerID, State: models.StateRunning, StartedAt: now,
                Players: []string{},
        }
        s.mu.Unlock()

        // first-run marker (.servy) — port reminder notice
        marker := filepath.Join(def.Path, ".servy")
        if _, err := os.Stat(marker); err != nil {
                s.deps.Bus.Publish(events.Event{Type: events.EvNotification, ServerID: def.ServerID,
                        Data: map[string]interface{}{"title": "Server started", "message": fmt.Sprintf(
                                "'%s' is running for the first time. Check the server port (%d) is open on your router if friends cannot connect.",
                                def.ServerName, derivePort(def, nil)), "user_id": userID}})
                os.WriteFile(marker, []byte(now.Format(time.RFC3339)), 0o644)
        }

        // wait for container exit
        go func() {
                code, werr := s.deps.Docker.WaitExit(ctx, id)
                select {
                case s.waitCh <- code:
                default:
                }
                s.handleExit(code, werr)
        }()

        s.setState(models.StateRunning)
        s.StartCollector()

        // flush queued commands
        s.mu.Lock()
        q := s.queue
        s.queue = nil
        s.mu.Unlock()
        for _, c := range q {
                s.writeStdin(c)
        }

        s.deps.Log.Get("server").Info("server %s (%s) started in container %s (image %s)", def.ServerName, def.ServerID, id[:12], spec.Image)
        s.deps.Log.Audit("", userID, def.ServerID, "", fmt.Sprintf("Started server %s (docker)", def.ServerName))
        s.deps.Bus.Publish(events.Event{Type: events.EvServerStart, ServerID: def.ServerID,
                Data: map[string]interface{}{"server_id": def.ServerID, "name": def.ServerName}})
        return nil
}

// stopDocker gracefully stops the container: stop command via attach stdin,
// poll for exit, then docker stop (SIGTERM) and SIGKILL as last resort.
func (s *Supervisor) stopDocker(reason string, userID int64, timeout time.Duration) error {
        s.mu.RLock()
        id := s.dockerID
        stdinLine := s.def.StopCommand
        action := s.def.StopAction
        s.mu.RUnlock()
        ctx := context.Background()
        if id == "" {
                s.setState(models.StateStopping)
                return nil
        }
        if action != "signal" && stdinLine != "" {
                _ = s.writeStdin(stdinLine)
                s.setState(models.StateStopping)
        } else {
                s.setState(models.StateStopping)
                stctx, cancel := context.WithTimeout(ctx, 15*time.Second)
                _ = s.deps.Docker.Stop(stctx, id, 15)
                cancel()
        }
        s.deps.Log.Get("server").Info("stopping server %s (%s, container)", s.def.ServerName, reason)

        deadline := time.Now().Add(timeout)
        tick := time.NewTicker(500 * time.Millisecond)
        defer tick.Stop()
        for {
                s.mu.RLock()
                dead := s.dead
                s.mu.RUnlock()
                if dead {
                        break
                }
                if time.Now().After(deadline) {
                        s.deps.Log.Get("server").Warning("server %s stop timeout reached — docker stop/kill", s.def.ServerName)
                        stctx, cancel := context.WithTimeout(ctx, 15*time.Second)
                        _ = s.deps.Docker.Stop(stctx, id, 15)
                        cancel()
                        time.Sleep(2 * time.Second)
                        s.mu.RLock()
                        stillDead := s.dead
                        s.mu.RUnlock()
                        if !stillDead {
                                kctx, kcancel := context.WithTimeout(ctx, 10*time.Second)
                                _ = s.deps.Docker.Kill(kctx, id)
                                kcancel()
                        }
                        break
                }
                <-tick.C
        }
        s.deps.Log.Audit("", userID, s.def.ServerID, "", fmt.Sprintf("Stopped server %s (%s)", s.def.ServerName, reason))
        s.deps.Bus.Publish(events.Event{Type: events.EvServerStop, ServerID: s.def.ServerID,
                Data: map[string]interface{}{"server_id": s.def.ServerID, "name": s.def.ServerName}})
        return nil
}

// sampleDockerStats takes a one-shot container stats sample and computes
// docker-style CPU% against the previous sample (kept per collector loop).
func (s *Supervisor) sampleDockerStats() (float64, uint64, float64) {
        if s.deps.Docker == nil {
                return 0, 0, 0
        }
        s.mu.RLock()
        id := s.dockerID
        s.mu.RUnlock()
        if id == "" {
                return 0, 0, 0
        }
        ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
        defer cancel()
        st, err := s.deps.Docker.Stats(ctx, id)
        if err != nil {
                return 0, 0, 0
        }
        cpu := dockerx.StatsPercent(s.lastDockerStats, st, time.Since(s.lastDockerAt).Nanoseconds())
        s.lastDockerStats = st
        s.lastDockerAt = time.Now()
        return cpu, st.Mem, st.MemPct
}

// ---------------- exit handling ----------------

// handleExit classifies process/container exit (clean vs crash) and drives
// auto-restart.
func (s *Supervisor) handleExit(code int, waitErr error) {
        s.mu.Lock()
        s.dead = true
        stopping := s.stopping
        s.stdinPipe = nil
        tty := s.tty
        s.tty = nil
        s.stdinW = nil
        cmd := s.cmd
        s.cmd = nil
        // Release under the same lock Kill/Stop/stats read Process.Pid with:
        // Release() sets Process.Pid = -1 and every other reader holds s.mu,
        // so no unlocked access can observe (or race) the mutation.
        if cmd != nil && cmd.Process != nil {
                _ = cmd.Process.Release()
        }
        // docker backend teardown
        dID := s.dockerID
        s.dockerID = ""
        dStdin := s.dockerStdin
        s.dockerStdin = nil
        dCancel := s.dockerCancel
        s.dockerCancel = nil
        crashCount := s.crashCount
        def := s.def
        s.mu.Unlock()
        _ = waitErr
        if tty != nil {
                tty.Close()
        }
        if dCancel != nil {
                dCancel() // closes the hijacked attach connection
        }
        if dStdin != nil {
                dStdin.Close()
        }
        if dID != "" && s.deps.Docker != nil && def.DockerAutoremove == 1 {
                go func() {
                        rctx, rcancel := context.WithTimeout(context.Background(), 15*time.Second)
                        defer rcancel()
                        _ = s.deps.Docker.Remove(rctx, dID)
                }()
        }

        // clean-exit classification
        ignored := parseCSV(def.IgnoredExits)
        cleanCode := false
        for _, x := range ignored {
                if strconv.Itoa(code) == x {
                        cleanCode = true
                        break
                }
        }
        if code == 0 {
                cleanCode = true
        }

        s.StopCollector()
        s.deps.Log.Get("server").Info("server %s exited with code %d (intentional=%v ignored=%v)",
                def.ServerName, code, stopping, cleanCode)

        if stopping || cleanCode {
                s.mu.Lock()
                s.stopping = false
                s.runtime.Crashed = false
                s.mu.Unlock()
                s.setState(models.StateStopped)
                return
        }

        // crash path
        s.mu.Lock()
        s.runtime.Crashed = true
        s.mu.Unlock()
        if def.CrashDetection == 1 && crashCount < 3 {
                s.crashCount = crashCount + 1
                s.setState(models.StateCrashed)
                s.deps.Log.Get("server").Error("server %s crashed (code %d); auto-restart %d/3", def.ServerName, code, s.crashCount)
                s.deps.Bus.Publish(events.Event{Type: events.EvServerCrash, ServerID: def.ServerID,
                        Data: map[string]interface{}{"server_id": def.ServerID, "name": def.ServerName, "exit_code": code}})
                time.Sleep(2 * time.Second)
                if err := s.Start(0); err != nil {
                        s.deps.Log.Get("server").Error("auto-restart of %s failed: %v", def.ServerName, err)
                        s.setState(models.StateCrashed)
                }
                return
        }
        s.setState(models.StateCrashed)
        s.deps.Log.Get("server").Error("server %s crashed (code %d) — giving up on auto-restart (count %d)", def.ServerName, code, crashCount)
        s.deps.Bus.Publish(events.Event{Type: events.EvServerCrash, ServerID: def.ServerID,
                Data: map[string]interface{}{"server_id": def.ServerID, "name": def.ServerName, "exit_code": code, "gave_up": true}})
}

// CrashReset clears the crash counter and crashed state.
func (s *Supervisor) CrashReset() {
        s.mu.Lock()
        s.crashCount = 0
        s.runtime.Crashed = false
        s.mu.Unlock()
        if s.State() != models.StateRunning {
                s.setState(models.StateStopped)
        }
}

// SetRuntime mutator for stats collectors.
func (s *Supervisor) SetRuntime(fn func(r *models.ServerRuntime)) {
        s.mu.Lock()
        fn(&s.runtime)
        s.mu.Unlock()
}

// UpdateFlag toggles the updating state.
func (s *Supervisor) SetUpdating(v bool) {
        s.mu.Lock()
        s.runtime.Updating = v
        if v {
                s.state = models.StateUpdating
        } else if s.state == models.StateUpdating {
                s.state = models.StateStopped
        }
        s.mu.Unlock()
}

// SetImporting toggles the importing state.
func (s *Supervisor) SetImporting(v bool) {
        s.mu.Lock()
        s.runtime.Importing = v
        if v {
                s.state = models.StateImporting
        } else if s.state == models.StateImporting {
                s.state = models.StateStopped
        }
        s.mu.Unlock()
}

// derivePort computes the game port: java reads server.properties#server-port;
// hytale parses --bind or port-3; others use the DB value.
func derivePort(def gen.Server, extra []string) int {
        switch def.Type {
        case models.TypeMinecraftJava:
                if data, err := os.ReadFile(filepath.Join(def.Path, "server.properties")); err == nil {
                        for _, line := range strings.Split(string(data), "\n") {
                                if strings.HasPrefix(strings.TrimSpace(line), "server-port=") {
                                        if p, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "server-port="))); err == nil {
                                                return p
                                        }
                                }
                        }
                }
        case models.TypeHytale:
                argv := splitCommand(def.ExecutionCommand)
                for i, a := range argv {
                        if a == "--bind" && i+1 < len(argv) {
                                if p, err := strconv.Atoi(argv[i+1]); err == nil {
                                        return p
                                }
                        }
                }
                return int(def.ServerPort) - 3
        }
        return int(def.ServerPort)
}

// GamePort returns the current game port (cached per lifecycle).
func (s *Supervisor) GamePort() int {
        s.mu.RLock()
        p := s.runtime.ServerPortSnapshot
        s.mu.RUnlock()
        if p != 0 {
                return p
        }
        p = derivePort(s.def, nil)
        s.mu.Lock()
        s.runtime.ServerPortSnapshot = p
        s.mu.Unlock()
        return p
}

// QueryPort resolves the Steam query (A2S) UDP port for this server. Most
// Source-engine games follow the game_port + 1 convention; titles that declare
// an explicit query port in the catalog (Abiotic Factor) use that instead.
func (s *Supervisor) QueryPort() int {
        if game, ok := s.gameManifest(s.def); ok && game.QueryPort > 0 {
                return game.QueryPort
        }
        return s.GamePort() + 1
}

// BackupLock lets the backup engine suppress restarts.
type BackupLock struct {
        mu   sync.Mutex
        held bool
}

// Hold marks a backup-with-shutdown in progress.
func (b *BackupLock) Hold() { b.mu.Lock(); b.held = true; b.mu.Unlock() }

// Release clears the flag.
func (b *BackupLock) Release() { b.mu.Lock(); b.held = false; b.mu.Unlock() }

// Held reports the flag.
func (b *BackupLock) Held() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.held }

func parseCSV(s *string) []string {
        if s == nil || *s == "" {
                return nil
        }
        out := []string{}
        for _, p := range strings.Split(*s, ",") {
                p = strings.TrimSpace(p)
                if p != "" {
                        out = append(out, p)
                }
        }
        return out
}
