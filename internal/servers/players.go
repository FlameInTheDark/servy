package servers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// PlayerTracker parses console output for join/leave events per server type
// and maintains a persistent cache (players_cache.json in the server config dir),
// mirroring the legacy cache_players.
type PlayerTracker struct {
	mu       sync.Mutex
	serverID string
	dataDir  string
	players  map[string]models_Player
	joined   map[string]bool
}

// models_Player is the on-disk player cache entry.
type models_Player struct {
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	LastSeen time.Time `json:"last_seen"`
}

var (
	javaJoinRe   = regexp.MustCompile(`(\w{1,16}) joined the game`)
	javaLeaveRe  = regexp.MustCompile(`(\w{1,16}) left the game`)
	legacyJoin   = regexp.MustCompile(`(\w{1,16})\[/[0-9.:]+\] logged in with entity id`)
	bedrockJoin  = regexp.MustCompile(`Player connected: (\w+), xuid:`)
	bedrockLeave = regexp.MustCompile(`Player disconnected: (\w+), xuid:`)
)

// NewPlayerTracker builds a tracker loading the existing cache.
func NewPlayerTracker(dataDir, serverID string) *PlayerTracker {
	t := &PlayerTracker{
		serverID: serverID, dataDir: dataDir,
		players: map[string]models_Player{}, joined: map[string]bool{},
	}
	if data, err := os.ReadFile(t.cachePath()); err == nil {
		var cache map[string]models_Player
		if json.Unmarshal(data, &cache) == nil {
			t.players = cache
			for n, p := range cache {
				t.joined[n] = p.Status == "online"
			}
		}
	}
	return t
}

func (t *PlayerTracker) cachePath() string {
	return filepath.Join(t.dataDir, "servers", t.serverID, "players_cache.json")
}

// Observe processes one console line (wired to the terminal onLine hook).
func (t *PlayerTracker) Observe(line string, serverType string) {
	var joins, leaves []string
	switch serverType {
	case "minecraft-bedrock":
		joins = bedrockJoin.FindStringSubmatch(line)[1:]
		leaves = bedrockLeave.FindStringSubmatch(line)[1:]
	default:
		if m := javaJoinRe.FindStringSubmatch(line); m != nil {
			joins = m[1:]
		} else if m := legacyJoin.FindStringSubmatch(line); m != nil {
			joins = m[1:]
		}
		if m := javaLeaveRe.FindStringSubmatch(line); m != nil {
			leaves = m[1:]
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now().UTC()
	for _, name := range joins {
		t.players[name] = models_Player{Name: name, Status: "online", LastSeen: now}
		t.joined[name] = true
	}
	for _, name := range leaves {
		t.players[name] = models_Player{Name: name, Status: "offline", LastSeen: now}
		delete(t.joined, name)
	}
	if len(joins)+len(leaves) > 0 {
		t.persist()
	}
}

// Online returns currently-online player names.
func (t *PlayerTracker) Online() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.joined))
	for n := range t.joined {
		out = append(out, n)
	}
	return out
}

// All returns the full cache (online + offline with last_seen).
func (t *PlayerTracker) All() []models_Player {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]models_Player, 0, len(t.players))
	for _, p := range t.players {
		if p.Status == "online" && !t.joined[p.Name] {
			p.Status = "offline"
		}
		out = append(out, p)
	}
	return out
}

// MarkAllOffline flips every player to offline (server stop).
func (t *PlayerTracker) MarkAllOffline() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for n := range t.players {
		t.players[n] = models_Player{Name: n, Status: "offline", LastSeen: time.Now().UTC()}
	}
	t.joined = map[string]bool{}
	t.persist()
}

func (t *PlayerTracker) persist() {
	data, err := json.MarshalIndent(t.players, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(t.cachePath()), 0o755)
	_ = os.WriteFile(t.cachePath(), data, 0o644)
}
