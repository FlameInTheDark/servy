package servers

// Server configuration patching: the single implementation shared by the
// panel API and cluster agents (remote servers are patched on their node).

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/FlameInTheDark/servy/internal/models"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
	"github.com/FlameInTheDark/servy/internal/steamcmd"
)

// ApplyServerPatch mutates def in place. Patches that carry user-facing launch
// parameters (Steam launch options, memory flags) are validated against the
// server's frozen manifest, so the panel API and cluster nodes reject bad
// values with the same message instead of storing something unusable.
func ApplyServerPatch(def *gen.Server, patch map[string]interface{}) error {
	if v, ok := patch["server_name"].(string); ok && v != "" {
		def.ServerName = v
	}
	if v, ok := patch["executable"].(string); ok && v != "" {
		def.Executable = v
	}
	if v, ok := patch["log_path"].(string); ok {
		def.LogPath = v
	}
	if v, ok := patch["execution_command"].(string); ok && v != "" {
		def.ExecutionCommand = v
	}
	if v, ok := patch["stop_command"].(string); ok {
		def.StopCommand = v
	}
	if v, ok := patch["stop_action"].(string); ok {
		def.StopAction = v
	}
	if v, ok := patch["io_mode"].(string); ok {
		def.IoMode = v
	}
	if v, ok := patch["server_ip"].(string); ok {
		def.ServerIp = v
	}
	if v, ok := patch["type"].(string); ok && v != "" {
		def.Type = v
	}
	if v, ok := patch["executable_update_url"].(string); ok {
		def.ExecutableUpdateUrl = &v
	}
	if v, ok := patch["auto_start"]; ok {
		def.AutoStart = boolI(v)
	}
	if v, ok := patch["auto_start_delay"]; ok {
		def.AutoStartDelay = numI(v, def.AutoStartDelay)
	}
	if v, ok := patch["crash_detection"]; ok {
		def.CrashDetection = boolI(v)
	}
	if v, ok := patch["shutdown_timeout"]; ok {
		def.ShutdownTimeout = numI(v, def.ShutdownTimeout)
	}
	if v, ok := patch["server_port"]; ok {
		def.ServerPort = numI(v, def.ServerPort)
	}
	if v, ok := patch["logs_delete_after"]; ok {
		def.LogsDeleteAfter = numI(v, def.LogsDeleteAfter)
	}
	if v, ok := patch["show_status"]; ok {
		def.ShowStatus = boolI(v)
	}
	if v, ok := patch["count_players"]; ok {
		def.CountPlayers = boolI(v)
	}
	if v, ok := patch["update_watcher"]; ok {
		def.UpdateWatcher = boolI(v)
	}
	if v, ok := patch["ignored_exits"].(string); ok {
		def.IgnoredExits = &v
	}
	// --- execution environment ---
	if v, ok := patch["shell"].(string); ok && ValidShellSelection(v) {
		def.Shell = v
	}
	if v, ok := patch["java_selection"].(string); ok {
		// legacy get_execution_java validation: "", "none", "java" or a
		// detected/absolute JVM path
		if v == "" || v == "none" || v == "java" || fileOrDirExists(v) {
			def.JavaSelection = v
		}
	}
	if v, ok := patch["execution_backend"].(string); ok {
		if v == "process" || v == "docker" {
			def.ExecutionBackend = v
		}
	}
	if v, ok := patch["docker_image"].(string); ok {
		def.DockerImage = &v
	}
	if v, ok := patch["docker_env"]; ok {
		if b, err := json.Marshal(toStringMap(v)); err == nil {
			s := string(b)
			def.DockerEnv = &s
		}
	}
	if v, ok := patch["docker_ports"]; ok {
		if b, err := json.Marshal(toStringSlice(v)); err == nil {
			s := string(b)
			def.DockerPorts = &s
		}
	}
	if v, ok := patch["docker_binds"]; ok {
		binds := []string{}
		for _, b := range toStringSlice(v) {
			// host side must be absolute, no traversal
			host := strings.SplitN(b, ":", 2)[0]
			if filepath.IsAbs(host) && !strings.Contains(host, "..") {
				binds = append(binds, b)
			}
		}
		if b, err := json.Marshal(binds); err == nil {
			s := string(b)
			def.DockerBinds = &s
		}
	}
	if v, ok := patch["docker_network"].(string); ok {
		def.DockerNetwork = &v
	}
	if v, ok := patch["docker_workdir"].(string); ok {
		if v == "" || (strings.HasPrefix(v, "/") && !strings.Contains(v, "..")) {
			def.DockerWorkdir = &v
		}
	}
	if v, ok := patch["docker_autoremove"]; ok {
		def.DockerAutoremove = boolI(v)
	}
	if def.Type == models.TypeSteamCmd {
		if snapshot, err := steamcmd.GameSnapshotFromDefinitions(def.Definitions); err == nil && snapshot != nil {
			def.Shell = "none"
		}
	}
	// Launch parameters edited after creation run after `server_port` above,
	// so automatic values (port, query port) validate against the new port.
	if raw, ok := patch["steam_options"]; ok && raw != nil {
		if err := applySteamOptions(def, raw); err != nil {
			return err
		}
	}
	if raw, ok := patch["min_mem"].(string); ok && strings.TrimSpace(raw) != "" {
		if err := rewriteMemoryFlag(def, "-Xms", raw); err != nil {
			return err
		}
	}
	if raw, ok := patch["max_mem"].(string); ok && strings.TrimSpace(raw) != "" {
		if err := rewriteMemoryFlag(def, "-Xmx", raw); err != nil {
			return err
		}
	}
	return nil
}

// applySteamOptions merges user-supplied values for the manifest launch options
// of a Steam server into its frozen game snapshot and re-validates them, so the
// panel API and cluster nodes accept exactly the values a start would accept.
func applySteamOptions(def *gen.Server, raw interface{}) error {
	snapshot, err := steamcmd.GameSnapshotFromDefinitions(def.Definitions)
	if err != nil {
		return fmt.Errorf("server launch configuration is invalid: %w", err)
	}
	if snapshot == nil {
		return errors.New("this server has no editable Steam launch parameters")
	}
	incoming, ok := raw.(map[string]interface{})
	if !ok {
		return errors.New("steam_options must be an object of option key to value")
	}
	known := make(map[string]steamcmd.LaunchOption, len(snapshot.Game.LaunchOptions))
	for _, option := range snapshot.Game.LaunchOptions {
		known[option.Key] = option
	}
	merged := make(map[string]string, len(snapshot.LaunchOptions)+len(incoming))
	for key, value := range snapshot.LaunchOptions {
		merged[key] = value
	}
	for key, value := range incoming {
		option, exists := known[key]
		if !exists {
			return fmt.Errorf("unknown launch option %q", key)
		}
		if option.Source != "" {
			return fmt.Errorf("%s is filled in by Servy automatically", option.Label)
		}
		text := fmt.Sprintf("%v", value)
		if value == nil || strings.TrimSpace(text) == "" {
			// clearing a value falls back to the manifest default
			delete(merged, key)
			continue
		}
		merged[key] = text
	}
	port := int(def.ServerPort)
	queryPort := snapshot.Game.QueryPort
	if queryPort == 0 {
		queryPort = port + 1
	}
	_, resolved, err := steamcmd.ResolveLaunchOptions(snapshot.Game.LaunchOptions, merged, steamcmd.LaunchContext{
		ServerName: def.ServerName, Port: port, QueryPort: queryPort,
		SaveDir: filepath.Join(def.Path, "saves"),
	})
	if err != nil {
		return err
	}
	snapshot.LaunchOptions = resolved
	encoded, err := steamcmd.EncodeGameSnapshot(*snapshot)
	if err != nil {
		return err
	}
	def.Definitions = encoded
	return nil
}

// memoryValuePattern matches the sizes Servy accepts for -Xms/-Xmx (512M, 2G,
// 1024, …) — the same shape the creation wizard asks for.
var memoryValuePattern = regexp.MustCompile(`^[0-9]{1,7}[KMGkmg]?$`)

// rewriteMemoryFlag replaces the value of one memory flag in the execution
// command. Servers whose command has no such flag (shell scripts, Bedrock,
// Steam games) get a clear error instead of a silent no-op.
func rewriteMemoryFlag(def *gen.Server, flag, value string) error {
	value = strings.TrimSpace(value)
	if !memoryValuePattern.MatchString(value) || strings.Trim(value, "0") == "" {
		return fmt.Errorf("%s must be a size such as 512M, 2G or 1024", flag)
	}
	command := def.ExecutionCommand
	if strings.TrimSpace(command) == "" {
		return errors.New("this server has no execution command to update")
	}
	pattern := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(flag) + `[0-9]+[KMG]?`)
	if !pattern.MatchString(command) {
		return fmt.Errorf("the execution command has no %s memory flag to update", flag)
	}
	def.ExecutionCommand = pattern.ReplaceAllString(command, flag+value)
	return nil
}

func boolI(v interface{}) int64 {
	switch x := v.(type) {
	case bool:
		if x {
			return 1
		}
	case float64:
		if x != 0 {
			return 1
		}
	}
	return 0
}

func numI(v interface{}, def int64) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	}
	return def
}

func fileOrDirExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func toStringMap(v interface{}) map[string]string {
	out := map[string]string{}
	switch x := v.(type) {
	case map[string]interface{}:
		for k, val := range x {
			if s, ok := val.(string); ok {
				out[k] = s
			} else {
				out[k] = fmt.Sprintf("%v", val)
			}
		}
	}
	return out
}

func toStringSlice(v interface{}) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []interface{}:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			} else {
				out = append(out, fmt.Sprintf("%v", item))
			}
		}
		return out
	case string:
		if x == "" {
			return []string{}
		}
		parts := strings.Split(x, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts
	}
	return []string{}
}
