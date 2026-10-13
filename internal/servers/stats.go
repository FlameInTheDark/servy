package servers

import (
        "encoding/json"
        "fmt"
        "os"
        "path/filepath"
        "runtime"
        "sort"
        "strings"
        "sync"
        "time"

        "github.com/shirou/gopsutil/v3/cpu"
        gpdisk "github.com/shirou/gopsutil/v3/disk"
        "github.com/shirou/gopsutil/v3/host"
        "github.com/shirou/gopsutil/v3/load"
        "github.com/shirou/gopsutil/v3/mem"
        gpproc "github.com/shirou/gopsutil/v3/process"

        "github.com/FlameInTheDark/servy/internal/database"
        "github.com/FlameInTheDark/servy/internal/events"
        "github.com/FlameInTheDark/servy/internal/models"
)

// Collector samples per-server process stats (5s) and persists rows at the
// configured frequency; also maintains world size on a slower cadence.
type Collector struct {
        sup  *Supervisor
        deps Deps
        stop chan struct{}
        once sync.Once
        wg   sync.WaitGroup
}

// StartCollector launches the per-server stats goroutines (called on Running).
func (s *Supervisor) StartCollector() {
        if s.collector != nil {
                return
        }
        c := &Collector{sup: s, deps: s.deps, stop: make(chan struct{})}
        s.collector = c
        c.wg.Add(2)
        go c.sampleLoop()
        go c.persistLoop()
}

// StopCollector terminates stats goroutines (called on exit).
func (s *Supervisor) StopCollector() {
        if s.collector == nil {
                return
        }
        s.collector.once.Do(func() { close(s.collector.stop) })
        s.collector.wg.Wait()
        s.collector = nil
}

// sampleLoop polls process stats every 5s and publishes WS server_stats.
func (c *Collector) sampleLoop() {
        defer c.wg.Done()
        tick := time.NewTicker(5 * time.Second)
        defer tick.Stop()
        procCache := map[int32]*gpproc.Process{}
        for {
                select {
                case <-c.stop:
                        return
                case <-tick.C:
                }
                s := c.sup
                if s.State() != models.StateRunning {
                        continue
                }
                var cpuPct float64
                var memBytes uint64
                var memPct float64
                if s.isDocker() {
                        cpuPct, memBytes, memPct = s.sampleDockerStats()
                } else {
                        s.mu.RLock()
                        pid := int32(0)
                        if s.cmd != nil && s.cmd.Process != nil {
                                pid = int32(s.cmd.Process.Pid)
                        }
                        s.mu.RUnlock()
                        if pid == 0 {
                                continue
                        }
                        p, ok := procCache[pid]
                        if !ok {
                                np, err := gpproc.NewProcess(pid)
                                if err != nil {
                                        continue
                                }
                                procCache[pid] = np
                                p = np
                        }
                        cpuPct, _ = p.Percent(0)
                        memInfo, _ := p.MemoryInfo()
                        memPct32, _ := p.MemoryPercent()
                        memPct = float64(memPct32)
                        if memInfo != nil {
                                memBytes = memInfo.RSS
                        }
                }
                rt := s.Runtime()
                s.SetRuntime(func(r *models.ServerRuntime) {
                        r.CPU = cpuPct
                        r.Mem = memBytes
                        r.MemPercent = float64(memPct)
                })
                // ping + players every cycle (cheap UDP/TCP probes)
                port := s.GamePort()
                ip := s.pingTarget()
                if s.def.CountPlayers == 1 || s.def.ShowStatus == 1 {
                        go func() {
                                info := PingServer(s.def.Type, ip, port, s.QueryPort())
                                if info != nil {
                                        s.SetRuntime(func(r *models.ServerRuntime) {
                                                r.Online = info.Online
                                                r.Max = info.Max
                                                r.Players = info.Players
                                                r.Version = info.Version
                                                r.Desc = info.Desc
                                                r.PingMS = info.LatencyMS
                                        })
                                }
                        }()
                }
                _ = rt
                // broadcast
                c.deps.Bus.Publish(events.Event{Type: events.EvServerStats, ServerID: s.def.ServerID,
                        Data: s.Runtime()})
        }
}

// persistLoop writes StatRecords to the DB at stats_update_frequency_seconds.
func (c *Collector) persistLoop() {
        defer c.wg.Done()
        freq := time.Duration(c.deps.Cfg.Get().StatsUpdateFrequencySeconds) * time.Second
        if freq < 5*time.Second {
                freq = 30 * time.Second
        }
        tick := time.NewTicker(freq)
        defer tick.Stop()
        worldTick := time.Duration(c.deps.Cfg.Get().DirSizePollFreqMinutes) * time.Minute
        if worldTick <= 0 {
                worldTick = 5 * time.Minute
        }
        worldTimer := time.NewTicker(worldTick)
        defer worldTimer.Stop()

        var lastWorldSize uint64
        for {
                select {
                case <-c.stop:
                        return
                case <-tick.C:
                        s := c.sup
                        rt := s.Runtime()
                        if !rt.Running && s.State() != models.StateRunning {
                                continue
                        }
                        players, _ := json.Marshal(rt.Players)
                        started := rt.StartedAt.Format(time.RFC3339)
                        worldName := ""
                        port := int64(s.GamePort())
                        desc, ver, icon := rt.Desc, rt.Version, ""
                        c.deps.DB.InsertServerStat(
                                s.def.ServerID, database.NowString(), &started, boolInt(s.State() == models.StateRunning),
                                rt.CPU, int64(rt.Mem), rt.MemPercent, &worldName, int64(rt.WorldSize), &port,
                                rt.PingMS, int64(rt.Online), int64(rt.Max), string(players), &desc, &ver,
                                boolInt(rt.Updating), boolInt(rt.WaitingStart), boolInt(rt.Crashed), boolInt(rt.Importing), &icon)
                case <-worldTimer.C:
                        s := c.sup
                        if s.State() == models.StateRunning {
                                size := DirSize(s.def.Path)
                                lastWorldSize = size
                                s.SetRuntime(func(r *models.ServerRuntime) { r.WorldSize = size })
                        } else if lastWorldSize > 0 {
                                s.SetRuntime(func(r *models.ServerRuntime) { r.WorldSize = lastWorldSize })
                        }
                }
        }
}

func boolInt(b bool) int64 {
        if b {
                return 1
        }
        return 0
}

// DirSize walks a directory summing file sizes (world size poll).
func DirSize(path string) uint64 {
        var size uint64
        filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
                if err != nil || info == nil {
                        return nil
                }
                if !info.IsDir() {
                        size += uint64(info.Size())
                }
                return nil
        })
        return size
}

// ---------------- Host stats ----------------

// HostStatsSnapshot is one host sample.
type HostStatsSnapshot struct {
        BootTime   string            `json:"boot_time"`
        CPUUsage   float64           `json:"cpu_usage"`
        CPUCores   int               `json:"cpu_cores"`
        CPUCurFreq float64           `json:"cpu_cur_freq"`
        CPUMaxFreq float64           `json:"cpu_max_freq"`
        MemPercent float64           `json:"mem_percent"`
        MemUsage   uint64            `json:"mem_usage"`
        MemTotal   uint64            `json:"mem_total"`
        Disks      []models.DiskStat `json:"disk_json"`
        Load       float64           `json:"load_avg"`
        OS         string            `json:"os"`
}

// HostStats collects host telemetry: broadcasts every 5s, persists every 30s.
type HostStats struct {
        deps     Deps
        stop     chan struct{}
        once     sync.Once
        mu       sync.RWMutex
        snapshot HostStatsSnapshot
}

// NewHostStats builds the collector.
func NewHostStats(deps Deps) *HostStats {
        return &HostStats{deps: deps, stop: make(chan struct{})}
}

// Start launches the host stats loops.
func (h *HostStats) Start() {
        go h.loop()
}

// Stop terminates collection.
func (h *HostStats) Stop() { h.once.Do(func() { close(h.stop) }) }

// Snapshot returns the latest host sample.
func (h *HostStats) Snapshot() HostStatsSnapshot {
        h.mu.RLock()
        defer h.mu.RUnlock()
        return h.snapshot
}

func (h *HostStats) loop() {
        persist := time.NewTicker(30 * time.Second)
        sample := time.NewTicker(5 * time.Second)
        defer persist.Stop()
        defer sample.Stop()
        h.collect()
        for {
                select {
                case <-h.stop:
                        return
                case <-sample.C:
                        h.collect()
                        h.deps.Bus.Publish(events.Event{Type: events.EvHostStats, Data: h.Snapshot()})
                case <-persist.C:
                        snap := h.Snapshot()
                        if bt, err := host.BootTime(); err == nil {
                                _ = bt
                        }
                        disksB, _ := json.Marshal(snap.Disks)
                        disks := string(disksB)
                        memU := float64(snap.MemUsage)
                        memT := float64(snap.MemTotal)
                        h.deps.DB.InsertHostStat(database.NowString(), snap.BootTime, snap.CPUUsage,
                                int64(snap.CPUCores), snap.CPUCurFreq, snap.CPUMaxFreq, snap.MemPercent,
                                memU, memT, &disks)
                }
        }
}

func (h *HostStats) collect() {
        snap := HostStatsSnapshot{Disks: []models.DiskStat{}}
        if pct, err := cpu.Percent(0, false); err == nil && len(pct) > 0 {
                snap.CPUUsage = pct[0]
        }
        if cores, err := cpu.Counts(true); err == nil {
                snap.CPUCores = cores
        }
        if freqs, err := cpu.Percent(0, false); err == nil {
                _ = freqs
        }
        // frequency info
        if freq, err := cpu.Info(); err == nil && len(freq) > 0 {
                snap.CPUCurFreq = freq[0].Mhz
                snap.CPUMaxFreq = freq[0].Mhz
                for _, f := range freq {
                        if f.Mhz > snap.CPUMaxFreq {
                                snap.CPUMaxFreq = f.Mhz
                        }
                }
        }
        if vm, err := mem.VirtualMemory(); err == nil {
                snap.MemPercent = vm.UsedPercent
                snap.MemUsage = vm.Used
                snap.MemTotal = vm.Total
        }
        if l, err := load.Avg(); err == nil && runtime.GOOS != "windows" {
                snap.Load = l.Load1
        }
        if bt, err := host.BootTime(); err == nil {
                snap.BootTime = time.Unix(int64(bt), 0).UTC().Format(time.RFC3339)
        }
        snap.OS = runtime.GOOS
        // monitored mounts (default: the data dir's device)
        mounts := h.deps.Cfg.Get().MonitoredMounts
        if len(mounts) == 0 {
                mounts = []string{"/"}
                if runtime.GOOS == "windows" {
                        mounts = []string{"C:\\"}
                }
        }
        for _, m := range mounts {
                if du, err := gpdisk.Usage(m); err == nil {
                        snap.Disks = append(snap.Disks, models.DiskStat{
                                Device: du.Fstype, Mount: m, Total: du.Total, Used: du.Used, Free: du.Free,
                                UsedPC: du.UsedPercent,
                        })
                }
        }
        if parts, err := gpdisk.Partitions(false); err == nil {
                _ = parts
        }
        h.mu.Lock()
        h.snapshot = snap
        h.mu.Unlock()
}

// History fetches per-server stats history with adaptive sampling
// (sampling tiers: <=6h:1, <=24h:1/2, <=72h:1/6, else fallback divisor).
func History(deps Deps, serverID string, hours int) []models.HistoryPoint {
        if hours <= 0 {
                hours = 24
        }
        since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339)
        rows, err := deps.DB.GetServerStatsSince(serverID, since)
        if err != nil {
                return nil
        }
        cfg := deps.Cfg.Get()
        rate := cfg.SamplingFallbackDivisor
        if rate <= 0 {
                rate = 12
        }
        for _, tier := range cfg.SamplingTiers {
                if hours <= tier.MaxHours {
                        rate = tier.SampleRate
                        break
                }
        }
        if rate <= 0 {
                rate = 1
        }
        out := []models.HistoryPoint{}
        for i, row := range rows {
                if i%rate != 0 {
                        continue
                }
                players := []string{}
                if row.Players != "" {
                        json.Unmarshal([]byte(row.Players), &players)
                }
                t, _ := time.Parse(time.RFC3339, row.Created)
                out = append(out, models.HistoryPoint{
                        Time: row.Created, CPU: row.Cpu, Mem: uint64(row.Mem), MemPct: row.MemPercent,
                        Online: int(row.Online), Max: int(row.Max), Ping: row.IntPingResults,
                        World: uint64(row.WorldSize), Running: row.Running == 1,
                })
                _ = t
        }
        return out
}

// HostHistory fetches host stats history.
func HostHistory(deps Deps, hours int) []models.HostStatPoint {
        if hours <= 0 {
                hours = 24
        }
        since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339)
        rows, err := deps.DB.GetHostStatsSince(since)
        if err != nil {
                return nil
        }
        out := []models.HostStatPoint{}
        for _, row := range rows {
                disks := []models.DiskStat{}
                if row.DiskJson != nil {
                        json.Unmarshal([]byte(*row.DiskJson), &disks)
                }
                out = append(out, models.HostStatPoint{
                        Time: row.Time, BootTime: row.BootTime, CPUUsage: row.CpuUsage, CPUCores: int(row.CpuCores),
                        CPUCurFreq: row.CpuCurFreq, CPUMaxFreq: row.CpuMaxFreq, MemPercent: row.MemPercent,
                        MemUsage: uint64(row.MemUsage), MemTotal: uint64(row.MemTotal), Disks: disks,
                })
        }
        return out
}

// LatestStat returns the most recent DB stat row for a server.
func LatestStat(deps Deps, serverID string) *models.StatRecord {
        row, err := deps.DB.GetLatestServerStat(serverID)
        if err != nil {
                return nil
        }
        players := []string{}
        if row.Players != "" {
                json.Unmarshal([]byte(row.Players), &players)
        }
        return &models.StatRecord{
                ServerID: row.ServerID, Created: mustTime(row.Created), Running: row.Running == 1,
                CPU: row.Cpu, Mem: uint64(row.Mem), MemPercent: row.MemPercent,
                WorldSize: uint64(row.WorldSize), ServerPort: derefInt64(row.ServerPort), PingMS: row.IntPingResults,
                Online: int(row.Online), Max: int(row.Max), Players: players, Desc: deref(row.Desc),
                Version: deref(row.Version), Updating: row.Updating == 1, WaitingStart: row.WaitingStart == 1,
                Crashed: row.Crashed == 1, Importing: row.Importing == 1,
        }
}

func mustTime(s string) time.Time {
        t, _ := time.Parse(time.RFC3339, s)
        return t
}

func deref(s *string) string {
        if s == nil {
                return ""
        }
        return *s
}

// sortPlayers is a helper for deterministic player lists.
func sortPlayers(in []string) []string {
        out := append([]string{}, in...)
        sort.Strings(out)
        return out
}

var _ = fmt.Sprintf
var _ = strings.TrimSpace

// derefInt64 handles nullable ints.
func derefInt64(v *int64) int {
        if v == nil {
                return 0
        }
        return int(*v)
}
