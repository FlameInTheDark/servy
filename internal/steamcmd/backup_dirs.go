package steamcmd

import (
	"encoding/json"
	"path"
	"strings"
)

// Backup folder lists — the folders a backup picks up.
//
// Two manifest systems feed this:
//
//   - the Steam catalog: `backup_dirs` in the game manifest, frozen into the
//     server's definitions as part of the game snapshot;
//   - creation recipes (configedit.Manifest): `backup_dirs`, stored in the same
//     definitions envelope for servers that were created from a recipe.
//
// The backup engines and the folder picker in the server's backup settings both
// read the list through these helpers, so a server keeps the folders its
// manifest declared even after the catalog or the recipe changes.

// NormalizeBackupDirs cleans a folder list: forward slashes, no leading or
// trailing separator, no absolute paths, no parent traversal, no duplicates,
// empty entries dropped. These relative paths are what the backup scope
// compares against the server directory.
func NormalizeBackupDirs(dirs []string) []string {
	out := make([]string, 0, len(dirs))
	seen := make(map[string]bool, len(dirs))
	for _, dir := range dirs {
		clean := CleanBackupDir(dir)
		if clean == "" || seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	return out
}

// CleanBackupDir normalizes one entry and returns "" when it cannot be used as
// a backup folder (absolute path, parent traversal, or nothing left).
func CleanBackupDir(dir string) string {
	clean := path.Clean(strings.ReplaceAll(strings.TrimSpace(dir), `\`, "/"))
	clean = strings.TrimPrefix(clean, "./")
	clean = strings.TrimSuffix(clean, "/")
	if clean == "" || clean == "." || clean == ".." {
		return ""
	}
	if strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return ""
	}
	return clean
}

// EncodeServerBackupDirs stores a manifest's backup folders (and the name of
// the manifest that declared them) in the definitions envelope, keeping every
// other field of that envelope. An empty list clears the folder restriction:
// backups then cover the whole server directory, exactly like servers created
// before this feature existed. The result is nil when nothing is left to store.
func EncodeServerBackupDirs(raw *string, dirs []string, source string) (*string, error) {
	definitions, _ := parseDefinitions(raw)
	definitions.BackupDirs = NormalizeBackupDirs(dirs)
	definitions.BackupDirsName = ""
	if len(definitions.BackupDirs) > 0 {
		definitions.BackupDirsName = strings.TrimSpace(source)
	}
	if definitions.SteamGame == nil && len(definitions.BackupDirs) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(definitions)
	if err != nil {
		return nil, err
	}
	value := string(data)
	return &value, nil
}

// BackupDirsFromDefinitions returns the folders the server's manifest selected
// for backup. A Steam server wins with its frozen game manifest; servers
// created from a recipe fall back to the recipe list. Non-JSON legacy
// definitions are ignored, like GameSnapshotFromDefinitions does.
func BackupDirsFromDefinitions(raw *string) []string {
	definitions, ok := parseDefinitions(raw)
	if !ok {
		return nil
	}
	if definitions.SteamGame != nil && len(definitions.SteamGame.Game.BackupDirs) > 0 {
		return NormalizeBackupDirs(definitions.SteamGame.Game.BackupDirs)
	}
	return NormalizeBackupDirs(definitions.BackupDirs)
}

// BackupDirsSource names the manifest that declared the folders, so the UI can
// label them ("Valheim" for a Steam game, the stored recipe name otherwise).
// It returns "" when the server has no manifest folder list.
func BackupDirsSource(raw *string) string {
	definitions, ok := parseDefinitions(raw)
	if !ok {
		return ""
	}
	if definitions.SteamGame != nil && len(definitions.SteamGame.Game.BackupDirs) > 0 {
		if name := strings.TrimSpace(definitions.SteamGame.Game.Name); name != "" {
			return name
		}
		return definitions.SteamGame.Game.Slug
	}
	return strings.TrimSpace(definitions.BackupDirsName)
}

func parseDefinitions(raw *string) (ServerDefinitions, bool) {
	var definitions ServerDefinitions
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return definitions, false
	}
	trimmed := strings.TrimSpace(*raw)
	if !strings.HasPrefix(trimmed, "{") {
		return definitions, false
	}
	if err := json.Unmarshal([]byte(trimmed), &definitions); err != nil {
		return definitions, false
	}
	return definitions, true
}
