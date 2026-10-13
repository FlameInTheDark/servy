// Package configedit implements the config editor framework: lossless drivers
// for .properties / .json / .yml / .toml-ish files, the Minecraft visual editor
// schema, and the manifest registry (URL-importable server definitions).
package configedit

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Comment preservation: properties driver keeps comments by tracking raw lines.

// PropertiesDoc represents a .properties file with ordered entries.
type PropertiesDoc struct {
	Order    []string
	Values   map[string]string
	Types    map[string]string // inferred type for the visual editor
	Comments []string          // header comments
}

// ParseProperties parses a java properties file.
func ParseProperties(data string) *PropertiesDoc {
	doc := &PropertiesDoc{Values: map[string]string{}, Types: map[string]string{}}
	for _, raw := range strings.Split(data, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
			doc.Comments = append(doc.Comments, line)
			continue
		}
		idx := strings.IndexAny(line, "=:")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if _, exists := doc.Values[key]; !exists {
			doc.Order = append(doc.Order, key)
		}
		doc.Values[key] = val
	}
	return doc
}

// RenderProperties serializes with stable ordering.
func RenderProperties(doc *PropertiesDoc) string {
	var b strings.Builder
	for _, c := range doc.Comments {
		b.WriteString(c + "\n")
	}
	for _, k := range doc.Order {
		b.WriteString(k + "=" + doc.Values[k] + "\n")
	}
	return b.String()
}

// Get returns a value.
func (d *PropertiesDoc) Get(key string) (string, bool) {
	v, ok := d.Values[key]
	return v, ok
}

// Set updates/creates a value.
func (d *PropertiesDoc) Set(key, value string) {
	if _, exists := d.Values[key]; !exists {
		d.Order = append(d.Order, key)
	}
	d.Values[key] = value
}

// Delete removes a key.
func (d *PropertiesDoc) Delete(key string) {
	delete(d.Values, key)
	for i, k := range d.Order {
		if k == key {
			d.Order = append(d.Order[:i], d.Order[i+1:]...)
			break
		}
	}
}

// ToForm converts to visual-editor form fields (ordered).
func (d *PropertiesDoc) ToForm() []FormField {
	fields := []FormField{}
	for _, k := range d.Order {
		fields = append(fields, FormField{
			Key: k, Value: d.Values[k], Type: inferType(d.Values[k]),
		})
	}
	return fields
}

// inferType guesses the visual editor input type from a value.
func inferType(v string) string {
	switch strings.ToLower(v) {
	case "true", "false":
		return "boolean"
	}
	if isIntStr(v) {
		return "integer"
	}
	if isFloatStr(v) {
		return "float"
	}
	return "string"
}

func isIntStr(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

func isFloatStr(s string) bool {
	if s == "" {
		return false
	}
	dots := 0
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c == '.':
			dots++
		case c == '-':
		default:
			return false
		}
	}
	return dots <= 1
}

// FormField is one visual editor field.
type FormField struct {
	Key     string   `json:"key"`
	Value   string   `json:"value"`
	Type    string   `json:"type"` // string | boolean | integer | float | select
	Options []Option `json:"options,omitempty"`
	Label   string   `json:"label,omitempty"`
	Help    string   `json:"help,omitempty"`
	Section string   `json:"section,omitempty"`
}

// Option is a select choice.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ---------------- Minecraft server.properties schema (visual editor) ----------------

// mcSchema sections for the visual editor.
var mcPropertiesSchema = []SchemaSection{
	{
		ID: "basic", Label: "Basic Settings",
		Fields: []SchemaField{
			{Key: "motd", Type: "string", Label: "Message of the Day (MOTD)", Help: "Shown in the server list. Supports § color codes."},
			{Key: "server-port", Type: "integer", Label: "Server Port", Help: "TCP port players connect to (default 25565)."},
			{Key: "server-ip", Type: "string", Label: "Server IP", Help: "Leave empty to bind on all interfaces."},
			{Key: "max-players", Type: "integer", Label: "Max Players"},
			{Key: "online-mode", Type: "boolean", Label: "Online Mode", Help: "Verify accounts with Mojang/Microsoft (recommended)."},
			{Key: "white-list", Type: "boolean", Label: "Whitelist"},
			{Key: "enforce-whitelist", Type: "boolean", Label: "Enforce Whitelist"},
			{Key: "pvp", Type: "boolean", Label: "PvP Enabled"},
			{Key: "allow-flight", Type: "boolean", Label: "Allow Flight"},
			{Key: "difficulty", Type: "select", Label: "Difficulty", Options: opts("peaceful", "easy", "normal", "hard")},
			{Key: "gamemode", Type: "select", Label: "Default Gamemode", Options: opts("survival", "creative", "adventure", "spectator")},
			{Key: "force-gamemode", Type: "boolean", Label: "Force Gamemode"},
			{Key: "hardcore", Type: "boolean", Label: "Hardcore Mode"},
			{Key: "allow-nether", Type: "boolean", Label: "Allow Nether"},
			{Key: "spawn-monsters", Type: "boolean", Label: "Spawn Monsters"},
			{Key: "spawn-animals", Type: "boolean", Label: "Spawn Animals"},
			{Key: "spawn-protection", Type: "integer", Label: "Spawn Protection Radius"},
		},
	},
	{
		ID: "performance", Label: "Performance",
		Fields: []SchemaField{
			{Key: "view-distance", Type: "integer", Label: "View Distance", Help: "Chunks sent to players (lower = less lag)."},
			{Key: "simulation-distance", Type: "integer", Label: "Simulation Distance"},
			{Key: "max-tick-time", Type: "integer", Label: "Max Tick Time (ms)"},
			{Key: "sync-chunk-writes", Type: "boolean", Label: "Sync Chunk Writes"},
			{Key: "network-compression-threshold", Type: "integer", Label: "Network Compression Threshold"},
			{Key: "rate-limit", Type: "integer", Label: "Packet Rate Limit"},
		},
	},
	{
		ID: "world", Label: "World",
		Fields: []SchemaField{
			{Key: "level-name", Type: "string", Label: "Level Name"},
			{Key: "level-seed", Type: "string", Label: "Level Seed"},
			{Key: "level-type", Type: "string", Label: "Level Type"},
			{Key: "generate-structures", Type: "boolean", Label: "Generate Structures"},
			{Key: "max-world-size", Type: "integer", Label: "Max World Size"},
			{Key: "max-build-height", Type: "integer", Label: "Max Build Height"},
			{Key: "enable-command-block", Type: "boolean", Label: "Enable Command Blocks"},
			{Key: "function-permission-level", Type: "integer", Label: "Function Permission Level"},
		},
	},
	{
		ID: "advanced", Label: "Advanced",
		Fields: []SchemaField{
			{Key: "enable-rcon", Type: "boolean", Label: "Enable RCON"},
			{Key: "rcon.port", Type: "integer", Label: "RCON Port"},
			{Key: "rcon.password", Type: "string", Label: "RCON Password"},
			{Key: "enable-query", Type: "boolean", Label: "Enable Query"},
			{Key: "query.port", Type: "integer", Label: "Query Port"},
			{Key: "enable-status", Type: "boolean", Label: "Enable Status"},
			{Key: "resource-pack", Type: "string", Label: "Resource Pack URL"},
			{Key: "resource-pack-sha1", Type: "string", Label: "Resource Pack SHA-1"},
			{Key: "require-resource-pack", Type: "boolean", Label: "Require Resource Pack"},
			{Key: "player-idle-timeout", Type: "integer", Label: "Player Idle Timeout"},
			{Key: "prevent-proxy-connections", Type: "boolean", Label: "Prevent Proxy Connections"},
			{Key: "use-native-transport", Type: "boolean", Label: "Use Native Transport"},
			{Key: "broadcast-rcon-to-ops", Type: "boolean", Label: "Broadcast RCON to Ops"},
			{Key: "broadcast-to-lua", Type: "boolean", Label: "Broadcast to Lua"},
			{Key: "op-permission-level", Type: "integer", Label: "Op Permission Level"},
			{Key: "text-filtering-config", Type: "string", Label: "Text Filtering Config"},
			{Key: "log-ips", Type: "boolean", Label: "Log IPs"},
		},
	},
}

// SchemaSection groups visual editor fields.
type SchemaSection struct {
	ID     string        `json:"id"`
	Label  string        `json:"label"`
	Fields []SchemaField `json:"fields"`
}

// SchemaField is one schema entry.
type SchemaField struct {
	Key     string   `json:"key"`
	Type    string   `json:"type"`
	Label   string   `json:"label"`
	Help    string   `json:"help,omitempty"`
	Options []Option `json:"options,omitempty"`
}

// MCPropertiesSchema returns the Minecraft server.properties schema.
func MCPropertiesSchema() []SchemaSection { return mcPropertiesSchema }

// BuildVisualForm merges a schema with actual file contents: schema fields in
// order + any extra keys (custom/unrecognized) appended.
func BuildVisualForm(schema []SchemaSection, doc *PropertiesDoc) map[string]interface{} {
	sections := []map[string]interface{}{}
	seen := map[string]bool{}
	for _, sec := range schema {
		fields := []FormField{}
		for _, f := range sec.Fields {
			seen[f.Key] = true
			val, ok := doc.Values[f.Key]
			if !ok {
				continue // not present in file — skip (visual editor edits what exists)
			}
			ff := FormField{Key: f.Key, Value: val, Type: f.Type, Label: f.Label, Help: f.Help, Section: sec.ID}
			for _, o := range f.Options {
				ff.Options = append(ff.Options, Option{Value: o.Value, Label: o.Label})
			}
			fields = append(fields, ff)
		}
		if len(fields) > 0 {
			sections = append(sections, map[string]interface{}{"id": sec.ID, "label": sec.Label, "fields": fields})
		}
	}
	// extra keys not in schema → "other" section
	extras := []FormField{}
	for _, k := range doc.Order {
		if seen[k] {
			continue
		}
		extras = append(extras, FormField{Key: k, Value: doc.Values[k], Type: inferType(doc.Values[k]), Section: "other"})
	}
	if len(extras) > 0 {
		sort.Slice(extras, func(i, j int) bool { return extras[i].Key < extras[j].Key })
		sections = append(sections, map[string]interface{}{"id": "other", "label": "Other Settings", "fields": extras})
	}
	return map[string]interface{}{"sections": sections}
}

// ApplyVisualForm writes form values back into a properties doc.
func ApplyVisualForm(doc *PropertiesDoc, values map[string]string) {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		doc.Set(k, values[k])
	}
}

// ---------------- JSON documents ----------------

// ValidateJSON checks a JSON document parses.
func ValidateJSON(data []byte) error {
	var v interface{}
	return json.Unmarshal(data, &v)
}

// FormatJSON pretty-prints JSON (canonical 2-space).
func FormatJSON(data []byte) ([]byte, error) {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return json.MarshalIndent(v, "", "  ")
}

// ---------------- Manifest registry ----------------

// Manifest is an importable server definition (URL auto-setup).
type Manifest struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	Version          string                 `json:"version"`
	Type             string                 `json:"type"`        // minecraft-java | minecraft-bedrock | steam_cmd | custom
	CreateType       string                 `json:"create_type"` // download_jar | import_zip | raw_exec | steam_game
	JarURL           string                 `json:"jar_url,omitempty"`
	SteamSlug        string                 `json:"steam_slug,omitempty"`
	AppID            string                 `json:"app_id,omitempty"`
	Port             int                    `json:"port,omitempty"`
	MinMem           string                 `json:"min_mem,omitempty"`
	MaxMem           string                 `json:"max_mem,omitempty"`
	StopCommand      string                 `json:"stop_command,omitempty"`
	ExecutionCommand string                 `json:"execution_command,omitempty"`
	Executable       string                 `json:"executable,omitempty"`
	Files            map[string]string      `json:"files,omitempty"`  // relative path -> URL/content
	Config           map[string]string      `json:"config,omitempty"` // server.properties overrides
	Env              map[string]interface{} `json:"env,omitempty"`    // env.json
}

// Registry validates + stores manifests (DB-backed via definitions table).
type Registry struct {
	fetchURL func(url string) ([]byte, error)
}

// NewRegistry builds the manifest registry.
func NewRegistry() *Registry {
	return &Registry{fetchURL: fetchHTTP}
}

// ParseManifest validates manifest bytes.
func (r *Registry) ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid manifest JSON: %v", err)
	}
	if m.Name == "" {
		return nil, fmt.Errorf("manifest missing name")
	}
	if m.ID == "" {
		m.ID = strings.ToLower(strings.ReplaceAll(m.Name, " ", "-"))
	}
	switch m.CreateType {
	case "download_jar":
		if m.JarURL == "" {
			return nil, fmt.Errorf("download_jar manifest requires jar_url")
		}
	case "steam_game":
		if m.SteamSlug == "" && m.AppID == "" {
			return nil, fmt.Errorf("steam manifest requires steam_slug or app_id")
		}
	case "import_zip", "raw_exec", "":
	default:
		return nil, fmt.Errorf("unknown create_type %q", m.CreateType)
	}
	return &m, nil
}

// FromURL fetches and validates a manifest from a remote URL.
func (r *Registry) FromURL(url string) (*Manifest, error) {
	data, err := r.fetchURL(url)
	if err != nil {
		return nil, err
	}
	return r.ParseManifest(data)
}

func fetchHTTP(url string) ([]byte, error) {
	client := &httpClientTimeout
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("manifest fetch: status %d", resp.StatusCode)
	}
	return readAllLimit(resp.Body, 4<<20)
}

// opts builds option lists concisely.
func opts(values ...string) []Option {
	out := make([]Option, 0, len(values))
	for _, v := range values {
		out = append(out, Option{Value: v, Label: strings.Title(v)})
	}
	return out
}
