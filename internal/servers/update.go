package servers

import (
	"archive/zip"
	"fmt"
	"github.com/FlameInTheDark/servy/internal/events"
	"github.com/FlameInTheDark/servy/internal/models"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}
	tmp := dest + ".download"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	os.Chmod(dest, 0o755)
	return nil
}

// updateBedrock fetches the latest bedrock dedicated server zip and extracts
// over the server directory (skipping world/ and server.properties).
func (u *UpdateManager) updateBedrock(serverPath string) error {
	// discover the real zip URL from the download page
	zipURL, err := u.discoverBedrockURL()
	if err != nil {
		return err
	}
	resp, err := http.Get(zipURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bedrock download returned %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp("", "bedrock-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		return err
	}
	tmp.Close()
	// open & extract over the dir
	zr, err := zip.OpenReader(tmp.Name())
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		name := filepath.Clean(f.Name)
		if strings.Contains(name, "..") {
			continue
		}
		// preserve user files
		if f.Name == "server.properties" || strings.HasPrefix(f.Name, "worlds/") {
			continue
		}
		dest := filepath.Join(serverPath, name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(dest, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(dest), 0o755)
		rc, err := f.Open()
		if err != nil {
			continue
		}
		out, err := os.Create(dest)
		if err != nil {
			rc.Close()
			continue
		}
		io.Copy(out, rc)
		out.Close()
		rc.Close()
		if isExecutableName(f.Name) {
			os.Chmod(dest, 0o755)
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
	client := &http.Client{Timeout: 15 * time.Second}
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
