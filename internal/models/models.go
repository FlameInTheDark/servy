// Package models holds shared domain types used across services and the API layer.
package models

import "time"

// Server types (mirrors the legacy server.type).
const (
	TypeMinecraftJava    = "minecraft-java"
	TypeMinecraftBedrock = "minecraft-bedrock"
	TypeRakNet           = "raknet"
	TypeSteamCmd         = "steam_cmd"
	TypeHytale           = "hytale"
	TypeCustom           = "custom"
)

// Server lifecycle states.
type ServerState string

const (
	StateStopped   ServerState = "stopped"
	StateStarting  ServerState = "starting"
	StateRunning   ServerState = "running"
	StateStopping  ServerState = "stopping"
	StateCrashed   ServerState = "crashed"
	StateUpdating  ServerState = "updating"
	StateImporting ServerState = "importing"
)

// ServerRuntime is the live (in-memory) view of a server beyond the DB config.
type ServerRuntime struct {
	ServerID     string      `json:"server_id"`
	State        ServerState `json:"state"`
	Crashed      bool        `json:"crashed"`
	CrashCounter int         `json:"crash_counter"`
	Updating     bool        `json:"updating"`
	Importing    bool        `json:"importing"`
	WaitingStart bool        `json:"waiting_start"`
	StartedAt    time.Time   `json:"started_at"`
	// Current stats snapshot
	CPU         float64  `json:"cpu"`
	Mem         uint64   `json:"mem"`
	MemPercent  float64  `json:"mem_percent"`
	Online      int      `json:"online"`
	Max         int      `json:"max"`
	Players     []string `json:"players"`
	Version     string   `json:"version"`
	Desc        string   `json:"desc"`
	WorldSize   uint64   `json:"world_size"`
	PingMS      float64  `json:"int_ping_results"`
	FirstRun    bool     `json:"first_run"`
	EULAPending bool     `json:"eula_pending"`
	Running     bool     `json:"running"`
	// ServerPortSnapshot caches the derived game port for this lifecycle.
	ServerPortSnapshot int `json:"-"`
}

// HistoryPoint is one sampled history row for charts.
type HistoryPoint struct {
	Time    string  `json:"time"`
	CPU     float64 `json:"cpu"`
	Mem     uint64  `json:"mem"`
	MemPct  float64 `json:"mem_percent"`
	Online  int     `json:"online"`
	Max     int     `json:"max"`
	Ping    float64 `json:"int_ping_results"`
	World   uint64  `json:"world_size"`
	Running bool    `json:"running"`
}

// HostStatPoint is one host telemetry sample.
type HostStatPoint struct {
	Time       string     `json:"time"`
	BootTime   string     `json:"boot_time"`
	CPUUsage   float64    `json:"cpu_usage"`
	CPUCores   int        `json:"cpu_cores"`
	CPUCurFreq float64    `json:"cpu_cur_freq"`
	CPUMaxFreq float64    `json:"cpu_max_freq"`
	MemPercent float64    `json:"mem_percent"`
	MemUsage   uint64     `json:"mem_usage"`
	MemTotal   uint64     `json:"mem_total"`
	Disks      []DiskStat `json:"disk_json"`
}

// DiskStat describes one mounted filesystem.
type DiskStat struct {
	Device string  `json:"device"`
	Mount  string  `json:"mount"`
	Total  uint64  `json:"total"`
	Used   uint64  `json:"used"`
	Free   uint64  `json:"free"`
	UsedPC float64 `json:"percent_used"`
}

// StatRecord is what the per-server stats collector produces.
type StatRecord struct {
	ServerID     string    `json:"server_id"`
	Created      time.Time `json:"created"`
	Started      string    `json:"started"`
	Running      bool      `json:"running"`
	CPU          float64   `json:"cpu"`
	Mem          uint64    `json:"mem"`
	MemPercent   float64   `json:"mem_percent"`
	WorldName    string    `json:"world_name"`
	WorldSize    uint64    `json:"world_size"`
	ServerPort   int       `json:"server_port"`
	PingMS       float64   `json:"int_ping_results"`
	Online       int       `json:"online"`
	Max          int       `json:"max"`
	Players      []string  `json:"players"`
	Desc         string    `json:"desc"`
	Version      string    `json:"version"`
	Updating     bool      `json:"updating"`
	WaitingStart bool      `json:"waiting_start"`
	Crashed      bool      `json:"crashed"`
	Importing    bool      `json:"importing"`
	Icon         string    `json:"icon"`
}

// Player is a cached player entry.
type Player struct {
	Name     string    `json:"name"`
	Status   string    `json:"status"` // online | offline
	LastSeen time.Time `json:"last_seen"`
}

// Now returns the canonical RFC3339 UTC timestamp used across the app.
func Now() string { return time.Now().UTC().Format(time.RFC3339) }
