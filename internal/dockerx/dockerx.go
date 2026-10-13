// Package dockerx is a thin, focused wrapper over the Docker Engine API used
// by the per-server "docker" execution backend and the SteamCMD runner.
//
// It talks to the local daemon (unix socket / named pipe / DOCKER_HOST) and
// covers exactly what servy needs: pull, run (with binds/ports/env/network),
// attach (multiplexed logs + raw stdin), wait, stop, kill, remove, one-shot
// jobs and one-shot stats.
package dockerx

import (
        "bufio"
        "context"
        "encoding/json"
        "errors"
        "fmt"
        "io"
        "os"
        "path/filepath"
        "runtime"
        "strings"
        "sync"
        "time"

        "github.com/docker/docker/api/types"
        "github.com/docker/docker/api/types/container"
        "github.com/docker/docker/api/types/image"
        "github.com/docker/docker/client"
        "github.com/docker/docker/pkg/stdcopy"
        "github.com/docker/go-connections/nat"
)

// ErrUnavailable is returned when the Docker daemon cannot be reached.
var ErrUnavailable = errors.New("DOCKER_UNAVAILABLE")

// InContainer reports whether this process itself runs inside a container
// (sibling-container setups need host-path translation for bind mounts).
func InContainer() bool {
        if _, err := os.Stat("/.dockerenv"); err == nil {
                return true
        }
        data, err := os.ReadFile("/proc/1/cgroup")
        if err != nil {
                return false
        }
        s := string(data)
        return strings.Contains(s, "docker") || strings.Contains(s, "containerd") ||
                strings.Contains(s, "kubepods") || strings.Contains(s, "lxc")
}

// ErrHostPathUnmapped is returned when a container-internal path under the
// data root cannot be translated to a host path (SERVY_HOST_DATA_PATH unset).
var ErrHostPathUnmapped = errors.New("SERVY_HOST_DATA_PATH not set: cannot map container paths to host paths for sibling containers")

// HostBindPath translates a path from servy's own filesystem view into a path
// the Docker daemon can bind-mount into a SIBLING container. When servy runs
// on the host the path is used as-is (absolutized). When servy runs inside a
// container, paths under the internal data root (SERVY_DATA_DIR, default
// /data) are re-rooted onto the host side of that mount (SERVY_HOST_DATA_PATH,
// set by docker-compose.yml). Paths outside the data root pass through
// unchanged (assumed to be host paths or host-mounted volumes).
func HostBindPath(p string) (string, error) {
        if !InContainer() {
                if abs, err := filepath.Abs(p); err == nil {
                        return abs, nil
                }
                return p, nil
        }
        hostBase := os.Getenv("SERVY_HOST_DATA_PATH")
        if hostBase == "" {
                return "", ErrHostPathUnmapped
        }
        if abs, err := filepath.Abs(p); err == nil {
                p = abs
        }
        root := os.Getenv("SERVY_DATA_DIR")
        if root == "" {
                root = "/data"
        }
        if absRoot, err := filepath.Abs(root); err == nil {
                if p == absRoot || strings.HasPrefix(p, absRoot+string(os.PathSeparator)) {
                        rel, rerr := filepath.Rel(absRoot, p)
                        if rerr == nil {
                                return filepath.Join(hostBase, rel), nil
                        }
                }
        }
        return p, nil
}

// CurrentUserSpec returns a "uid[:gid]" string for the current process, used
// to run sibling containers with the SAME uid as servy so they can write into
// its bind-mounted server directories (the #1 failure mode of docker-in-docker
// style setups: image default users cannot write host-owned mounts).
// Returns "" on non-Linux (Docker Desktop handles mapping itself) or when the
// uid cannot be determined.
func CurrentUserSpec() string {
        if runtime.GOOS != "linux" {
                return ""
        }
        uid := os.Getuid()
        gid := os.Getgid()
        if uid < 0 {
                return ""
        }
        if gid >= 0 {
                return fmt.Sprintf("%d:%d", uid, gid)
        }
        return fmt.Sprintf("%d", uid)
}

// RunSpec describes one managed container run.
type RunSpec struct {
        Name       string            // container name (e.g. "servy-<uuid>")
        Image      string            // image reference (required)
        Cmd        []string          // command + args (from the execution command)
        Env        []string          // KEY=value entries
        User       string            // "uid[:gid]" override ("" = image default)
        WorkingDir string            // inside the container (default "/data")
        Binds      []string          // [host:]container[:ro] mounts
        Ports      []string          // "host:container/proto" port specs
        Network    string            // network name ("" = default bridge)
        AutoRemove bool              // remove container on exit
        Labels     map[string]string // applied to the container
}

// Manager wraps the docker client (nil-safe: a nil Manager reports
// unavailable on every call).
type Manager struct {
        cli  *client.Client
        host string

        mu    sync.Mutex
        pings map[string]bool // cached image presence
}

// New builds a Manager from the environment (DOCKER_HOST, DOCKER_API_VERSION).
// Returns (nil, ErrUnavailable) when the daemon is unreachable — call sites
// treat a nil manager as "docker not available on this host".
func New() (*Manager, error) {
        cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
        if err != nil {
                return nil, err
        }
        ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
        defer cancel()
        if _, err := cli.Ping(ctx); err != nil {
                cli.Close()
                return nil, ErrUnavailable
        }
        host := cli.DaemonHost()
        return &Manager{cli: cli, host: host, pings: map[string]bool{}}, nil
}

// Host returns the daemon endpoint the manager is bound to.
func (m *Manager) Host() string {
        if m == nil {
                return ""
        }
        return m.host
}

// Available reports whether the daemon answers a ping.
func (m *Manager) Available() bool {
        if m == nil {
                return false
        }
        ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
        defer cancel()
        _, err := m.cli.Ping(ctx)
        return err == nil
}

// Version returns daemon version info ("" on error).
func (m *Manager) Version() string {
        if m == nil {
                return ""
        }
        ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
        defer cancel()
        v, err := m.cli.ServerVersion(ctx)
        if err != nil {
                return ""
        }
        return fmt.Sprintf("%s (API %s)", v.Version, v.APIVersion)
}

// EnsureImage pulls spec.Image unless it already exists locally. Progress
// lines are streamed to log (may be nil).
func (m *Manager) EnsureImage(ctx context.Context, ref string, log func(string)) error {
        if m == nil {
                return ErrUnavailable
        }
        m.mu.Lock()
        known := m.pings[ref]
        m.mu.Unlock()
        if known {
                return nil
        }
        if _, _, err := m.cli.ImageInspectWithRaw(ctx, ref); err == nil {
                m.mu.Lock()
                m.pings[ref] = true
                m.mu.Unlock()
                return nil
        }
        logf := func(string) {}
        if log != nil {
                logf = log
        }
        logf("pulling image " + ref + " …")
        reader, err := m.cli.ImagePull(ctx, ref, image.PullOptions{})
        if err != nil {
                return fmt.Errorf("image pull failed: %w", err)
        }
        defer reader.Close()
        dec := json.NewDecoder(reader)
        for {
                var msg struct {
                        Status         string `json:"status"`
                        ID             string `json:"id"`
                        ProgressDetail struct {
                                Current int64 `json:"current"`
                                Total   int64 `json:"total"`
                        } `json:"progressDetail"`
                }
                if err := dec.Decode(&msg); err != nil {
                        if err == io.EOF {
                                break
                        }
                        return err
                }
                if msg.ID != "" && msg.ProgressDetail.Total > 0 && msg.ProgressDetail.Current == msg.ProgressDetail.Total {
                        logf(fmt.Sprintf("%s: %s", msg.ID, msg.Status))
                } else if msg.ID == "" && msg.Status != "" {
                        logf(msg.Status)
                }
        }
        m.mu.Lock()
        m.pings[ref] = true
        m.mu.Unlock()
        return nil
}

// buildPortMap converts port specs ("25565:25565/tcp", "19132/udp") into
// exposed ports + host bindings.
func buildPortMap(specs []string) (nat.PortSet, nat.PortMap, error) {
        clean := make([]string, 0, len(specs))
        for _, raw := range specs {
                if s := strings.TrimSpace(raw); s != "" {
                        clean = append(clean, s)
                }
        }
        return nat.ParsePortSpecs(clean)
}

// Run creates + starts a container described by spec, removing any stale
// container with the same name first. Returns the container id.
func (m *Manager) Run(ctx context.Context, spec RunSpec) (string, error) {
        if m == nil {
                return "", ErrUnavailable
        }
        if spec.Image == "" {
                return "", errors.New("docker_image required")
        }
        if spec.WorkingDir == "" {
                spec.WorkingDir = "/data"
        }
        // remove stale same-name container (force: any state)
        _ = m.cli.ContainerRemove(ctx, spec.Name, container.RemoveOptions{Force: true, RemoveVolumes: true})

        exposed, bindings, err := buildPortMap(spec.Ports)
        if err != nil {
                return "", err
        }
        cfg := &container.Config{
                Image:      spec.Image,
                Cmd:        spec.Cmd,
                Env:        spec.Env,
                User:       spec.User,
                WorkingDir: spec.WorkingDir,
                Labels:     spec.Labels,
                OpenStdin:  true, // keep stdin open for console commands
                Tty:        false,
                // ExposedPorts is REQUIRED for publishing: the daemon only maps
                // ports declared here — HostConfig.PortBindings entries for
                // undeclared ports are silently ignored (moby
                // daemon/container_operations.go iterates Config.ExposedPorts).
                ExposedPorts: exposed,
        }
        hostCfg := &container.HostConfig{
                Binds:        spec.Binds,
                PortBindings: bindings,
                AutoRemove:   spec.AutoRemove,
        }
        if spec.Network != "" {
                hostCfg.NetworkMode = container.NetworkMode(spec.Network)
        }
        res, err := m.cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, spec.Name)
        if err != nil {
                return "", err
        }
        if err := m.cli.ContainerStart(ctx, res.ID, container.StartOptions{}); err != nil {
                _ = m.cli.ContainerRemove(ctx, res.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
                return "", err
        }
        return res.ID, nil
}

// Attach streams the container's merged stdout/stderr and returns a stdin
// writer. stdout is demultiplexed from the docker stream framing. The read
// side ends when the container stops; the returned cancel closes the hijacked
// connection.
func (m *Manager) Attach(ctx context.Context, containerID string) (stdout io.Reader, stdin io.WriteCloser, cancel func(), err error) {
        if m == nil {
                return nil, nil, nil, ErrUnavailable
        }
        resp, err := m.cli.ContainerAttach(ctx, containerID, container.AttachOptions{
                Stream: true,
                Stdin:  true,
                Stdout: true,
                Stderr: true,
                Logs:   true, // replay output produced since container start
        })
        if err != nil {
                return nil, nil, nil, err
        }
        pr, pw := io.Pipe()
        go func() {
                _, cerr := stdcopy.StdCopy(pw, pw, resp.Reader)
                pw.CloseWithError(cerr)
        }()
        actx, acancel := context.WithCancel(ctx)
        go func() {
                <-actx.Done()
                resp.Close()
        }()
        return pr, &connWriter{conn: resp.Conn}, acancel, nil
}

// connWriter adapts a hijacked net.Conn into an io.WriteCloser.
type connWriter struct {
        conn interface {
                Write([]byte) (int, error)
                Close() error
        }
}

func (w *connWriter) Write(p []byte) (int, error) { return w.conn.Write(p) }
func (w *connWriter) Close() error                { return w.conn.Close() }

// WaitExit blocks until the container exits and returns its status code.
func (m *Manager) WaitExit(ctx context.Context, containerID string) (int, error) {
        if m == nil {
                return -1, ErrUnavailable
        }
        ch, errCh := m.cli.ContainerWait(ctx, containerID, container.WaitConditionNextExit)
        select {
        case res := <-ch:
                return int(res.StatusCode), nil
        case err := <-errCh:
                return -1, err
        case <-ctx.Done():
                return -1, ctx.Err()
        }
}

// Stop sends SIGTERM; after the grace timeout the daemon SIGKILLs.
func (m *Manager) Stop(ctx context.Context, containerID string, timeoutSecs int) error {
        if m == nil {
                return ErrUnavailable
        }
        t := timeoutSecs
        return m.cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &t})
}

// Kill sends SIGKILL immediately.
func (m *Manager) Kill(ctx context.Context, containerID string) error {
        if m == nil {
                return ErrUnavailable
        }
        return m.cli.ContainerKill(ctx, containerID, "SIGKILL")
}

// Remove force-removes a container.
func (m *Manager) Remove(ctx context.Context, containerID string) error {
        if m == nil {
                return ErrUnavailable
        }
        return m.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true, RemoveVolumes: true})
}

// IsRunning inspects the container state.
func (m *Manager) IsRunning(ctx context.Context, containerID string) bool {
        if m == nil {
                return false
        }
        info, err := m.cli.ContainerInspect(ctx, containerID)
        if err != nil {
                return false
        }
        return info.State != nil && info.State.Running
}

// ContainerIPs returns the container's IP addresses on its docker networks.
// Empty (nil) when the container is not running or has no networks attached.
// The panel uses this to query game servers (A2S / RakNet / Java status)
// directly on the docker bridge: published ports only help a host-networked
// panel, while the container IP is reachable from the host AND from a
// containerized panel that shares the network.
func (m *Manager) ContainerIPs(ctx context.Context, containerID string) ([]string, error) {
        if m == nil {
                return nil, ErrUnavailable
        }
        info, err := m.cli.ContainerInspect(ctx, containerID)
        if err != nil {
                return nil, err
        }
        if info.NetworkSettings == nil {
                return nil, nil
        }
        var ips []string
        for _, ep := range info.NetworkSettings.Networks {
                if ep != nil && ep.IPAddress != "" {
                        ips = append(ips, ep.IPAddress)
                }
        }
        return ips, nil
}

// ContainerStats is one one-shot stats sample of a container.
type ContainerStats struct {
        CPU         float64 `json:"cpu"` // percent of all cores (delta-based; 0 in single samples)
        Mem         uint64  `json:"mem"` // bytes
        MemPct      float64 `json:"mem_percent"`
        CPUTotal    uint64  `json:"cpu_total"`    // cumulative CPU ns (for caller-side deltas)
        SystemTotal uint64  `json:"system_total"` // cumulative system ns
        OnlineCPUs  int     `json:"online_cpus"`
}

// Stats takes a one-shot stats sample. CPU percent requires two samples —
// callers diff CPUTotal/SystemTotal across their own poll interval (the
// one-shot API ships a zeroed pre-sample).
func (m *Manager) Stats(ctx context.Context, containerID string) (*ContainerStats, error) {
        if m == nil {
                return nil, ErrUnavailable
        }
        resp, err := m.cli.ContainerStatsOneShot(ctx, containerID)
        if err != nil {
                return nil, err
        }
        defer resp.Body.Close()
        var st types.StatsJSON
        if err := json.NewDecoder(bufio.NewReader(resp.Body)).Decode(&st); err != nil {
                return nil, err
        }
        out := &ContainerStats{
                CPUTotal:    st.CPUStats.CPUUsage.TotalUsage,
                SystemTotal: st.CPUStats.SystemUsage,
        }
        out.OnlineCPUs = int(st.CPUStats.OnlineCPUs)
        if out.OnlineCPUs == 0 {
                out.OnlineCPUs = len(st.CPUStats.CPUUsage.PercpuUsage)
        }
        used := st.MemoryStats.Usage
        if cache, ok := st.MemoryStats.Stats["cache"]; ok && used > cache {
                used -= cache
        }
        limit := st.MemoryStats.Limit
        out.Mem = used
        if limit > 0 {
                out.MemPct = float64(used) / float64(limit) * 100
        }
        return out, nil
}

// StatsPercent computes docker-style CPU percent from two one-shot samples
// taken deltaNs apart (wall clock between the two calls).
func StatsPercent(prev, cur *ContainerStats, deltaNs int64) float64 {
        if prev == nil || cur == nil || deltaNs <= 0 || cur.OnlineCPUs <= 0 {
                return 0
        }
        cpuDelta := int64(cur.CPUTotal) - int64(prev.CPUTotal)
        sysDelta := int64(cur.SystemTotal) - int64(prev.SystemTotal)
        if cpuDelta <= 0 || sysDelta <= 0 {
                return 0
        }
        return float64(cpuDelta) / float64(sysDelta) * float64(cur.OnlineCPUs) * 100
}

// JobResult is the outcome of a one-shot container job.
type JobResult struct {
        ExitCode int
        Output   []string
}

// RunJob creates, starts, streams output from and removes a one-shot
// container (used for SteamCMD install/update inside a container and other
// maintenance tasks). onLine receives each demuxed output line live.
func (m *Manager) RunJob(ctx context.Context, spec RunSpec, onLine func(string)) (*JobResult, error) {
        if m == nil {
                return nil, ErrUnavailable
        }
        if spec.Name == "" {
                spec.Name = fmt.Sprintf("servy-job-%d", time.Now().UnixNano())
        }
        if err := m.EnsureImage(ctx, spec.Image, onLine); err != nil {
                return nil, err
        }
        spec.AutoRemove = false
        id, err := m.Run(ctx, spec)
        if err != nil {
                return nil, err
        }
        defer func() {
                rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
                defer cancel()
                _ = m.Remove(rctx, id)
        }()
        stdout, _, cancel, err := m.Attach(ctx, id)
        if err != nil {
                return nil, err
        }
        defer cancel()
        out := []string{}
        done := make(chan struct{})
        go func() {
                defer close(done)
                br := bufio.NewScanner(stdout)
                br.Buffer(make([]byte, 64*1024), 1024*1024)
                for br.Scan() {
                        line := br.Text()
                        out = append(out, line)
                        if len(out) > 2000 {
                                out = out[len(out)-2000:]
                        }
                        if onLine != nil {
                                onLine(line)
                        }
                }
        }()
        code, err := m.WaitExit(ctx, id)
        if err != nil {
                return nil, err
        }
        // Ensure bind mount is fully synced to host before removing container
        _, _ = m.Run(context.Background(), RunSpec{
                Image:      "busybox:latest",
                Cmd:        []string{"sync"},
                Binds:      spec.Binds,
                AutoRemove: true,
        })
        select {
        case <-done:
        case <-time.After(5 * time.Second):
        }
        return &JobResult{ExitCode: code, Output: out}, nil
}