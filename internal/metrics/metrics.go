// Package metrics exposes Prometheus-format metrics: /metrics (host + go
// runtime) and /metrics/servers/{id} (per-server gauges filtered by label).
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/FlameInTheDark/servy/internal/models"
	"github.com/FlameInTheDark/servy/internal/servers"
)

// Collector caches the latest runtime snapshots for exposition.
type Collector struct {
	mu       sync.RWMutex
	runtimes map[string]models.ServerRuntime
	host     *servers.HostStats
}

// New builds the collector.
func New() *Collector {
	return &Collector{runtimes: map[string]models.ServerRuntime{}}
}

// UpdateServer records a server runtime snapshot.
func (c *Collector) UpdateServer(id string, r models.ServerRuntime) {
	c.mu.Lock()
	c.runtimes[id] = r
	c.mu.Unlock()
}

// SetHost wires the host stats source.
func (c *Collector) SetHost(h *servers.HostStats) {
	c.mu.Lock()
	c.host = h
	c.mu.Unlock()
}

// Gather builds the exposition payload. serverID == "" renders host metrics
// (all servers included, labeled); a non-empty id filters to that server.
func (c *Collector) Gather(serverID string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var b strings.Builder

	write := func(name, help, typ string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	gauge := func(name string, labels string, v float64) {
		if labels != "" {
			fmt.Fprintf(&b, "%s{%s} %g\n", name, labels, v)
		} else {
			fmt.Fprintf(&b, "%s %g\n", name, v)
		}
	}

	if serverID == "" {
		write("servy_host_cpu_percent", "Host CPU usage percent", "gauge")
		write("servy_host_mem_percent", "Host memory usage percent", "gauge")
		write("servy_host_mem_usage_bytes", "Host used memory bytes", "gauge")
		write("servy_host_mem_total_bytes", "Host total memory bytes", "gauge")
		if c.host != nil {
			snap := c.host.Snapshot()
			gauge("servy_host_cpu_percent", "", snap.CPUUsage)
			gauge("servy_host_mem_percent", "", snap.MemPercent)
			gauge("servy_host_mem_usage_bytes", "", float64(snap.MemUsage))
			gauge("servy_host_mem_total_bytes", "", float64(snap.MemTotal))
			for _, d := range snap.Disks {
				lbl := fmt.Sprintf("mount=%q", d.Mount)
				fmt.Fprintf(&b, "# HELP servy_host_disk_usage_bytes Disk usage\n# TYPE servy_host_disk_usage_bytes gauge\n")
				gauge("servy_host_disk_usage_bytes", lbl, float64(d.Used))
				fmt.Fprintf(&b, "# HELP servy_host_disk_total_bytes Disk total\n# TYPE servy_host_disk_total_bytes gauge\n")
				gauge("servy_host_disk_total_bytes", lbl, float64(d.Total))
			}
		}
	}

	ids := make([]string, 0, len(c.runtimes))
	for id := range c.runtimes {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	write("servy_server_running", "Server running state (1/0)", "gauge")
	write("servy_server_cpu_percent", "Server process CPU percent", "gauge")
	write("servy_server_mem_bytes", "Server process memory bytes", "gauge")
	write("servy_server_players_online", "Players online", "gauge")
	write("servy_server_players_max", "Player slots", "gauge")
	write("servy_server_ping_ms", "Internal ping milliseconds", "gauge")
	write("servy_server_crashed", "Server crashed flag", "gauge")
	write("servy_server_world_size_bytes", "Server directory size bytes", "gauge")

	for _, id := range ids {
		if serverID != "" && id != serverID {
			continue
		}
		r := c.runtimes[id]
		lbl := fmt.Sprintf("server_id=%q", id)
		running := 0.0
		if r.Running {
			running = 1
		}
		crashed := 0.0
		if r.Crashed {
			crashed = 1
		}
		gauge("servy_server_running", lbl, running)
		gauge("servy_server_cpu_percent", lbl, r.CPU)
		gauge("servy_server_mem_bytes", lbl, float64(r.Mem))
		gauge("servy_server_players_online", lbl, float64(r.Online))
		gauge("servy_server_players_max", lbl, float64(r.Max))
		gauge("servy_server_ping_ms", lbl, r.PingMS)
		gauge("servy_server_crashed", lbl, crashed)
		gauge("servy_server_world_size_bytes", lbl, float64(r.WorldSize))
	}
	return b.String()
}

// Handler returns the HTTP handler for /metrics.
func (c *Collector) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Write([]byte(c.Gather("")))
	}
}
