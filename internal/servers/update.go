package servers

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/FlameInTheDark/servy/internal/events"
	"github.com/FlameInTheDark/servy/internal/files"
	"github.com/FlameInTheDark/servy/internal/models"
	"github.com/FlameInTheDark/servy/internal/safehttp"
)

// UpdateManager handles executable updates per server type:
// java (URL download), bedrock (latest zip from Mojang), steam (app_update),
// hytale (re-run installer — delegated to creation service).
type UpdateManager struct {
	deps Deps
	reg  *Registry
}

// NewUpdateManager builds the updater.
func NewUpdateManager(deps Deps, reg *Registry) *UpdateManager {
	return &UpdateManager{deps: deps, reg: reg}
}

var bedrockDownloadURLs = []string{
	// Mojang publishes the latest dedicated-server zip; the URL changes per
	// release, discovered from the bedrock dedicated server page.
	"https://www.minecraft.net/en-us/download/server/bedrock",
}

// Update performs the update flow: stop if running → default backup →
// download per type → restart if it was running.
func (u *UpdateManager) Update(serverID string, userID int64, runBackup func(serverID string) error) error {
	sup, err := u.reg.Get(serverID)
	if err != nil {
		return err
	}
	def := sup.Definition()
	if sup.State() == models.StateRunning {
		sup.SetUpdating(true)
	}
	wasRunning := sup.State() == models.StateRunning
	if wasRunning {
		if err := sup.Stop("update", userID); err != nil && err != ErrServerNotRunning {
			sup.SetUpdating(false)
			return err
		}
	}
	sup.SetUpdating(true)
	defer sup.SetUpdating(false)

	// backup-first guard (original behavior: update aborts if backup fails)
	if runBackup != nil {
		if err := runBackup(serverID); err != nil {
			return fmt.Errorf("UPDATE_ABORTED_BACKUP_FAILED: %v", err)
		}
	}

	var updateErr error
	switch def.Type {
	case "minecraft-java":
		updateErr = u.updateFromURL(derefStr(def.ExecutableUpdateUrl), filepath.Join(def.Path, def.Executable))
	case "minecraft-bedrock":
		updateErr = u.updateBedrock(def.Path)
	case "steam_cmd":
		updateErr = fmt.Errorf("steam updates run via steamcmd app_update (use the SteamCMD action)")
	default:
		if def.ExecutableUpdateUrl != nil && *def.ExecutableUpdateUrl != "" {
			updateErr = u.updateFromURL(derefStr(def.ExecutableUpdateUrl), filepath.Join(def.Path, def.Executable))
		} else {
			updateErr = fmt.Errorf("no update URL configured for this server type")
		}
	}
	if updateErr != nil {
		u.deps.Log.Get("server").Error("update of %s failed: %v", def.ServerName, updateErr)
		return fmt.Errorf("UPDATE_FAILED: %v", updateErr)
	}
	u.deps.Log.Audit("", userID, serverID, "", fmt.Sprintf("Updated server %s", def.ServerName))
	u.deps.Bus.Publish(events.Event{Type: events.EvServerUpdate, ServerID: serverID,
		Data: map[string]interface{}{"server_id": serverID, "name": def.ServerName}})
	if wasRunning {
		time.Sleep(2 * time.Second)
		return sup.Start(userID)
	}
	return nil
}

// updateFromURL downloads a file (jar) overwriting dest.
func (u *UpdateManager) updateFromURL(url, dest string) error {
	if url == "" {
		return fmt.Errorf("no executable_update_url set")
	}
	tmp := dest + ".download"
	_ = os.Remove(tmp)
	if _, err := files.DownloadURL(url, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(dest, 0o755); err != nil {
		return err
	}
	return nil
}

// updateBedrock fetches the latest bedrock dedicated server zip and extracts
// over the server directory (skipping world/ and server.properties).
func (u *UpdateManager) updateBedrock(serverPath string) error {
	const maxArchiveBytes int64 = 2 << 30
	const maxExpandedBytes int64 = 8 << 30
	const maxEntries = 100_000

	zipURL, err := u.discoverBedrockURL()
	if err != nil {
		return err
	}
	client := safehttp.NewClient(15 * time.Minute)
	resp, err := client.Get(zipURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bedrock download returned %d", resp.StatusCode)
	}
	if resp.ContentLength > maxArchiveBytes {
		return fmt.Errorf("bedrock archive exceeds the 2 GiB download limit")
	}
	tmp, err := os.CreateTemp("", "bedrock-*.zip")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	written, err := io.Copy(tmp, io.LimitReader(resp.Body, maxArchiveBytes+1))
	if err != nil {
		tmp.Close()
		return err
	}
	if written > maxArchiveBytes {
	tmp.Close()
		return fmt.Errorf("bedrock archive exceeds the 2 GiB download limit")
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	zr, err := zip.OpenReader(tmpName)
	if err != nil {
		return err
	}
	defer zr.Close()
	if len(zr.File) > maxEntries {
		return fmt.Errorf("bedrock archive contains more than %d entries", maxEntries)
	}
	type archiveEntry struct {
		file *zip.File
		name string
		skip bool
	}
	entries := make([]archiveEntry, 0, len(zr.File))
	var declaredBytes uint64
	for _, f := range zr.File {
		name := path.Clean(strings.ReplaceAll(f.Name, `\`, "/"))
		drivePath := len(name) >= 2 && ((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= 'A' && name[0] <= 'Z')) && name[1] == ':'
		if strings.ContainsRune(name, '\x00') || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || drivePath {
			return files.ErrTraversal
		}
		mode := f.Mode()
		if !mode.IsRegular() && !mode.IsDir() {
			return fmt.Errorf("bedrock archive entry %q is not a regular file or directory", f.Name)
		}
		skip := name == "server.properties" || strings.HasPrefix(name, "worlds/")
		if !skip && !mode.IsDir() {
			if f.UncompressedSize64 > uint64(maxExpandedBytes)-declaredBytes {
				return fmt.Errorf("bedrock archive expands beyond the 8 GiB extraction limit")
			}
			declaredBytes += f.UncompressedSize64
		}
		entries = append(entries, archiveEntry{file: f, name: name, skip: skip})
	}

	serverRoot, err := filepath.Abs(serverPath)
	if err != nil {
		return fmt.Errorf("resolve server directory: %w", err)
	}
	var extractedBytes int64
	for _, entry := range entries {
		if entry.skip || entry.name == "." || entry.name == "" {
			continue
		}
		target := filepath.Join(serverRoot, filepath.FromSlash(entry.name))
		targetRel, err := filepath.Rel(serverRoot, target)
		if err != nil || targetRel == ".." || strings.HasPrefix(targetRel, ".."+string(os.PathSeparator)) {
			return files.ErrTraversal
		}
		if entry.file.Mode().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := entry.file.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return err
		}
		remaining := maxExpandedBytes - extractedBytes
		n, copyErr := io.Copy(out, io.LimitReader(rc, remaining+1))
		closeOutErr := out.Close()
		closeInErr := rc.Close()
		extractedBytes += n
		if extractedBytes > maxExpandedBytes {
			return fmt.Errorf("bedrock archive expands beyond the 8 GiB extraction limit")
		}
		if copyErr != nil {
			return copyErr
		}
		if closeOutErr != nil {
			return closeOutErr
		}
		if closeInErr != nil {
			return closeInErr
		}
		if uint64(n) != entry.file.UncompressedSize64 {
			return fmt.Errorf("bedrock archive entry %q has an inconsistent uncompressed size", entry.name)
		}
		if isExecutableName(entry.name) {
			if err := os.Chmod(target, 0o755); err != nil {
				return err
		}
		}
	}
	return nil
}

func isExecutableName(name string) bool {
	n := strings.ToLower(filepath.Base(name))
	return n == "bedrock_server" || n == "bedrock_server.exe" || strings.HasSuffix(n, ".sh")
}

// discoverBedrockURL scrapes the Mojang page for the linux dedicated server link.
func (u *UpdateManager) discoverBedrockURL() (string, error) {
	client := safehttp.NewClient(15 * time.Second)
	for _, page := range bedrockDownloadURLs {
		resp, err := client.Get(page)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		html := string(body)
		// find https://minecraft.azureedge.net/bin-linux/bedrock-server-*.zip
		const marker = "https://"
		pos := 0
		for {
			idx := strings.Index(html[pos:], marker)
			if idx < 0 {
				break
			}
			start := pos + idx
			end := strings.IndexAny(html[start:], "\"'< ")
			if end < 0 {
				break
			}
			cand := html[start : start+end]
			if strings.Contains(cand, "bin-linux/bedrock-server") && strings.HasSuffix(cand, ".zip") {
				return cand, nil
			}
			pos = start + end
		}
	}
	return "", fmt.Errorf("could not discover bedrock download URL (offline?)")
}

// derefStr handles nullable strings.
func derefStr(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
