package steamcmd

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const MaxGameManifestSize = 1 << 20

var gameSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$|^[a-z0-9]$`)

// ParseGameManifest parses a game-server manifest from YAML or JSON, rejects
// unknown fields, and validates fields needed to install and run the server.
func ParseGameManifest(data []byte) (Game, error) {
	if len(data) == 0 {
		return Game{}, fmt.Errorf("manifest is empty")
	}
	if len(data) > MaxGameManifestSize {
		return Game{}, fmt.Errorf("manifest exceeds the %d-byte limit", MaxGameManifestSize)
	}

	var game Game
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&game); err != nil {
		return Game{}, fmt.Errorf("parse game manifest: %w", err)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Game{}, fmt.Errorf("manifest must contain exactly one YAML document")
		}
		return Game{}, fmt.Errorf("parse game manifest: %w", err)
	}
	game.Type = "steam_cmd"
	if err := ValidateGame(game); err != nil {
		return Game{}, err
	}
	return game, nil
}

// ValidateGame checks the structural and safety invariants for a SteamCMD
// server definition. It does not check that the Steam app or command exists.
func ValidateGame(game Game) error {
	if game.AppID <= 0 || game.AppID > 2_147_483_647 {
		return fmt.Errorf("app_id must be a positive 32-bit Steam application ID")
	}
	if !gameSlugPattern.MatchString(game.Slug) {
		return fmt.Errorf("slug must be 1-64 lowercase letters, numbers, or hyphens and start/end with a letter or number")
	}
	if strings.TrimSpace(game.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if len(game.Name) > 120 {
		return fmt.Errorf("name must be no longer than 120 characters")
	}
	if strings.TrimSpace(game.RunCommand) == "" {
		return fmt.Errorf("run_command is required")
	}
	if strings.ContainsRune(game.RunCommand, '\x00') {
		return fmt.Errorf("run_command must not contain a NUL byte")
	}
	if strings.TrimSpace(game.Executable) == "" {
		return fmt.Errorf("executable is required")
	}
	if strings.ContainsRune(game.Executable, '\x00') {
		return fmt.Errorf("executable must not contain a NUL byte")
	}
	if game.Port < 1 || game.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if game.QueryPort < 0 || game.QueryPort > 65535 {
		return fmt.Errorf("query_port must be between 0 and 65535")
	}
	if game.QueryPort == 0 && game.Port == 65535 {
		return fmt.Errorf("port 65535 cannot use the default query port (game port + 1); set query_port explicitly")
	}
	if game.Platform != "" && game.Platform != "linux" && game.Platform != "windows" {
		return fmt.Errorf("platform must be empty, linux, or windows")
	}
	if game.WindowsOnly && game.Platform != "windows" {
		return fmt.Errorf("windows_only manifests must set platform: windows")
	}
	if game.DockerImage != "" && strings.TrimSpace(game.DockerImage) == "" {
		return fmt.Errorf("docker_image must not be blank")
	}

	for i, exposed := range game.Expose {
		if exposed.Offset < 0 || exposed.Offset > 65535 {
			return fmt.Errorf("expose[%d].offset must be between 0 and 65535", i)
		}
		if exposed.Port < 0 || exposed.Port > 65535 {
			return fmt.Errorf("expose[%d].port must be between 1 and 65535, or omitted to use the game port plus offset", i)
		}
		if exposed.Proto != "" && exposed.Proto != "tcp" && exposed.Proto != "udp" {
			return fmt.Errorf("expose[%d].proto must be tcp or udp", i)
		}
		resolved := exposed.Resolve(game.Port)
		if resolved < 1 || resolved > 65535 {
			return fmt.Errorf("expose[%d] resolves to invalid port %d; check port and offset", i, resolved)
		}
	}

	if len(game.ConfigFiles) > 128 {
		return fmt.Errorf("config_files may contain at most 128 paths")
	}
	seenConfigFiles := make(map[string]bool, len(game.ConfigFiles))
	for i, entry := range game.ConfigFiles {
		if err := validateRelativeManifestPath(entry); err != nil {
			return fmt.Errorf("config_files[%d]: %w", i, err)
		}
		clean := path.Clean(strings.ReplaceAll(entry, `\`, "/"))
		if seenConfigFiles[clean] {
			return fmt.Errorf("config_files contains duplicate path %q", clean)
		}
		seenConfigFiles[clean] = true
	}
	if len(game.ConfigEditors) > 128 {
		return fmt.Errorf("config_editors may contain at most 128 entries")
	}
	seenConfigEditors := make(map[string]bool, len(game.ConfigEditors))
	for i, editor := range game.ConfigEditors {
		if err := validateRelativeManifestPath(editor.Path); err != nil {
			return fmt.Errorf("config_editors[%d].path: %w", i, err)
		}
		clean := path.Clean(strings.ReplaceAll(editor.Path, `\`, "/"))
		if seenConfigEditors[clean] {
			return fmt.Errorf("config_editors contains duplicate path %q", clean)
		}
		seenConfigEditors[clean] = true
		if utf8.RuneCountInString(editor.Label) > 100 {
			return fmt.Errorf("config_editors[%d].label must be at most 100 characters", i)
		}
		if utf8.RuneCountInString(editor.Description) > 500 {
			return fmt.Errorf("config_editors[%d].description must be at most 500 characters", i)
		}
		switch editor.Editor {
		case "", "auto", "raw", "lines":
		default:
			return fmt.Errorf("config_editors[%d].editor must be auto, raw, or lines", i)
		}
	}
	for label, paths := range map[string][]string{
		"save_dirs":   game.SaveDirs,
		"backup_dirs": game.BackupDirs,
	} {
		for i, entry := range paths {
			if err := validateRelativeManifestPath(entry); err != nil {
				return fmt.Errorf("%s[%d]: %w", label, i, err)
			}
		}
	}
	if err := validateLaunchOptions(game.LaunchOptions); err != nil {
		return err
	}

	for key, values := range game.Env {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "=\x00\r\n") {
			return fmt.Errorf("env contains an invalid variable name")
		}
		for _, value := range values {
			if strings.ContainsRune(value, '\x00') {
				return fmt.Errorf("env value for %q contains a NUL byte", key)
			}
		}
	}
	return nil
}

func validateRelativeManifestPath(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("path must not be empty")
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("path must not contain a NUL byte")
	}
	clean := path.Clean(strings.ReplaceAll(value, `\`, "/"))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return fmt.Errorf("path must stay inside the server directory")
	}
	if len(clean) >= 2 && ((clean[0] >= 'a' && clean[0] <= 'z') || (clean[0] >= 'A' && clean[0] <= 'Z')) && clean[1] == ':' {
		return fmt.Errorf("path must be relative to the server directory")
	}
	return nil
}
