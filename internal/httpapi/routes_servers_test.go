package httpapi

import (
        "encoding/json"
        "testing"
        "time"

        "github.com/FlameInTheDark/servy/internal/models"
        "github.com/FlameInTheDark/servy/internal/repo/gen"
)

// statsKeys are the runtime fields the UI renders directly (Dashboard
// ServerCard, ServerDetail header). A missing key previously produced
// `undefined.toFixed` crashes in the browser for stopped/remote servers.
var statsKeys = []string{
        "state", "running", "crashed", "updating", "importing",
        "cpu", "mem", "mem_percent", "online", "max", "players",
        "version", "desc", "world_size", "int_ping_results",
        "eula_pending", "started_at",
}

// TestServerViewCompleteShape guards the /servers API contract: the stats
// block must ALWAYS be present, even when there is no runtime (stopped,
// remote-managed or just-created server). Regression test for the
// "Cannot read properties of undefined (reading 'toFixed')" dashboard crash.
func TestServerViewCompleteShape(t *testing.T) {
        a := &App{}

        // 1) No runtime (stopped / remote / not yet registered) → full zeroed shape.
        v := a.serverView(gen.Server{}, nil)
        for _, k := range statsKeys {
                if _, ok := v[k]; !ok {
                        t.Errorf("serverView without runtime is missing key %q — UI would crash on undefined", k)
                }
        }
        if v["state"] != "stopped" {
                t.Errorf("state = %v, want stopped", v["state"])
        }
        if v["running"] != false {
                t.Errorf("running = %v, want false", v["running"])
        }
        if cpu, ok := v["cpu"].(float64); !ok || cpu != 0 {
                t.Errorf("cpu = %v (%T), want float64(0)", v["cpu"], v["cpu"])
        }
        if players, ok := v["players"].([]string); !ok || players == nil {
                t.Errorf("players = %v, want non-nil empty slice", v["players"])
        }
        if v["started_at"] != nil {
                t.Errorf("started_at = %v, want nil for never-started server", v["started_at"])
        }

        // 2) Runtime present → live values flow through untouched.
        started := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
        rt := &models.ServerRuntime{
                State: models.StateRunning, Running: true,
                CPU: 12.5, Mem: 256 << 20, MemPercent: 3.25, Online: 4, Max: 20,
                Players: []string{"steve"}, Version: "1.21", Desc: "a server",
                WorldSize: 1024, PingMS: 33.3, StartedAt: started,
        }
        v = a.serverView(gen.Server{}, rt)
        for _, k := range statsKeys {
                if _, ok := v[k]; !ok {
                        t.Errorf("serverView with runtime is missing key %q", k)
                }
        }
        if v["cpu"] != 12.5 || v["online"] != 4 || v["max"] != 20 {
                t.Errorf("stats not passed through: cpu=%v online=%v max=%v", v["cpu"], v["online"], v["max"])
        }
        if v["running"] != true || v["state"] != "running" {
                t.Errorf("running=%v state=%v, want true/running", v["running"], v["state"])
        }
        if v["started_at"] != started {
                t.Errorf("started_at = %v, want %v", v["started_at"], started)
        }

        // 3) Runtime with zero StartedAt (stopped after restart cycle) → null, not
        // the Go zero-time string "0001-01-01T00:00:00Z".
        rt.StartedAt = time.Time{}
        v = a.serverView(gen.Server{}, rt)
        if v["started_at"] != nil {
                t.Errorf("started_at = %v, want nil for zero start time", v["started_at"])
        }

        // 4) The shape must serialize without surprises (map values may hold
        // time.Time / nil which json.Marshal must handle).
        if _, err := json.Marshal(v); err != nil {
                t.Fatalf("serverView result is not JSON-serializable: %v", err)
        }
}

// TestHostStatPointJSONContract pins the wire format of GET /panel/stats
// history rows: the CPU field is `cpu_usage` (NOT `cpu` — that is the
// per-server history field). The Dashboard host chart maps these values
// directly; renaming the tag silently breaks the panel.
func TestHostStatPointJSONContract(t *testing.T) {
        b, err := json.Marshal(models.HostStatPoint{
                Time: "2026-10-12T10:00:00Z", CPUUsage: 12.5, MemPercent: 42.0,
        })
        if err != nil {
                t.Fatalf("marshal: %v", err)
        }
        var m map[string]any
        if err := json.Unmarshal(b, &m); err != nil {
                t.Fatalf("unmarshal: %v", err)
        }
        if m["cpu_usage"] != 12.5 {
                t.Errorf("cpu_usage = %v, want 12.5", m["cpu_usage"])
        }
        if _, ok := m["cpu"]; ok {
                t.Error("host history must not expose a `cpu` key (that is the per-server field name)")
        }
        if m["mem_percent"] != 42.0 {
                t.Errorf("mem_percent = %v, want 42", m["mem_percent"])
        }
        if m["time"] != "2026-10-12T10:00:00Z" {
                t.Errorf("time = %v", m["time"])
        }
}
