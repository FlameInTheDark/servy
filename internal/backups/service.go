// Package backups implements the legacy backup engines: zip_vault (compressed
// archives with excludes + retention) and snapshot (content-addressed file
// store with manifests — deduplicated restores).
package backups

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/FlameInTheDark/servy/internal/database"
	"github.com/FlameInTheDark/servy/internal/events"
	"github.com/FlameInTheDark/servy/internal/files"
	"github.com/FlameInTheDark/servy/internal/logging"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
	"github.com/FlameInTheDark/servy/internal/servers"
)

// ErrNotFound / taxonomy.
var (
	ErrNotFound = errors.New("BACKUP_NOT_FOUND")
	ErrDisabled = errors.New("BACKUP_DISABLED")
)

// Service orchestrates backup configs, execution and restore.
type Service struct {
	db  *database.Queries
	log *logging.Manager
	bus *events.Bus
	reg *servers.Registry
}

// New builds the service.
func New(db *database.Queries, log *logging.Manager, bus *events.Bus, reg *servers.Registry) *Service {
	return &Service{db: db, log: log, bus: bus, reg: reg}
}

// ConfigView is the API shape of a backup config.
type ConfigView struct {
	BackupID       string                 `json:"backup_id"`
	Created        string                 `json:"created"`
	BackupName     string                 `json:"backup_name"`
	BackupLocation string                 `json:"backup_location"`
	ExcludedDirs   []string               `json:"excluded_dirs"`
	MaxBackups     int64                  `json:"max_backups"`
	ServerID       string                 `json:"server_id"`
	Compress       bool                   `json:"compress"`
	Shutdown       bool                   `json:"shutdown"`
	Before         string                 `json:"before,omitempty"`
	After          string                 `json:"after,omitempty"`
	Default        bool                   `json:"default"`
	Status         map[string]interface{} `json:"status"`
	Enabled        bool                   `json:"enabled"`
	BackupType     string                 `json:"backup_type"`
}

// View converts a DB row.
func (s *Service) View(b gen.Backup) ConfigView {
	v := ConfigView{
		BackupID: b.BackupID, Created: b.Created, BackupName: b.BackupName,
		BackupLocation: b.BackupLocation, MaxBackups: b.MaxBackups, ServerID: b.ServerID,
		Compress: b.Compress == 1, Shutdown: b.Shutdown == 1, Default: b.Default == 1,
		Enabled: b.Enabled == 1, BackupType: b.BackupType, ExcludedDirs: []string{},
		Status: map[string]interface{}{},
	}
	if b.ExcludedDirs != nil {
		for _, d := range strings.Split(*b.ExcludedDirs, ",") {
			if d = strings.TrimSpace(d); d != "" {
				v.ExcludedDirs = append(v.ExcludedDirs, d)
			}
		}
	}
	if b.Before != nil {
		v.Before = *b.Before
	}
	if b.After != nil {
		v.After = *b.After
	}
	if b.Status != nil && *b.Status != "" {
		json.Unmarshal([]byte(*b.Status), &v.Status)
	}
	return v
}

// Create adds a backup config.
func (s *Service) Create(b gen.Backup) (gen.Backup, error) {
	if b.BackupID == "" {
		b.BackupID = uuid.NewString()
	}
	b.Created = database.Now()
	if b.BackupType == "" {
		b.BackupType = "zip_vault"
	}
	return s.db.CreateBackup(b.BackupID, b.Created, b.BackupName, b.BackupLocation, b.ExcludedDirs,
		b.MaxBackups, b.ServerID, b.Compress, b.Shutdown, b.Before, b.After, b.Default, b.Status, b.Enabled, b.BackupType)
}

// List returns configs for a server.
func (s *Service) List(serverID string) []ConfigView {
	rows, _ := s.db.ListBackupsForServer(serverID)
	out := []ConfigView{}
	for _, r := range rows {
		out = append(out, s.View(r))
	}
	return out
}

// Get fetches one config.
func (s *Service) Get(backupID string) (gen.Backup, error) {
	return s.db.GetBackup(backupID)
}

// Update mutates a config.
func (s *Service) Update(b gen.Backup) error {
	return s.db.UpdateBackup(b.BackupName, b.BackupLocation, b.ExcludedDirs, b.MaxBackups, b.Compress,
		b.Shutdown, b.Before, b.After, b.Default, b.Status, b.Enabled, b.BackupType, b.BackupID)
}

// Delete removes a config (and its files if requested).
func (s *Service) Delete(backupID string, removeFiles bool) error {
	b, err := s.db.GetBackup(backupID)
	if err != nil {
		return ErrNotFound
	}
	if removeFiles && b.BackupLocation != "" {
		os.RemoveAll(b.BackupLocation)
	}
	return s.db.DeleteBackup(backupID)
}

// Run executes a backup now: shutdown option → before cmd → engine → after cmd
// → retention → restart-if-was-running. Publishes backup_start/backup_end.
func (s *Service) Run(backupID string, userID int64) error {
	b, err := s.db.GetBackup(backupID)
	if err != nil {
		return ErrNotFound
	}
	if b.Enabled != 1 {
		return ErrDisabled
	}
	sup, err := s.reg.Get(b.ServerID)
	if err != nil {
		return err
	}
	s.setStatus(b.BackupID, "running", true)
	s.bus.Publish(events.Event{Type: events.EvBackupStart, ServerID: b.ServerID,
		Data: map[string]interface{}{"server_id": b.ServerID, "name": sup.Definition().ServerName, "backup_id": backupID}})
	defer func() {
		s.setStatus(b.BackupID, "running", false)
	}()

	wasRunning := sup.State() == "running"
	lock := &servers.BackupLock{}
	lock.Hold()
	defer lock.Release()

	if b.Shutdown == 1 && wasRunning {
		if err := sup.Stop("backup", userID); err != nil && err != servers.ErrServerNotRunning {
			return err
		}
	}
	if b.Before != nil && *b.Before != "" {
		_ = sup.SendCommand(*b.Before, userID)
	}

	var backupErr error
	os.MkdirAll(b.BackupLocation, 0o755)
	if b.BackupType == "snapshot" {
		backupErr = s.runSnapshot(b, sup.Definition())
	} else {
		backupErr = s.runZipVault(b, sup.Definition())
	}

	if b.After != nil && *b.After != "" && wasRunning {
		_ = sup.SendCommand(*b.After, userID)
	}
	if backupErr != nil {
		s.log.Get("backup").Error("backup %s failed: %v", backupID, backupErr)
		s.setStatus(b.BackupID, "last_error", backupErr.Error())
		s.bus.Publish(events.Event{Type: events.EvBackupDone, ServerID: b.ServerID,
			Data: map[string]interface{}{"ok": false, "error": backupErr.Error()}})
		return backupErr
	}
	s.setStatus(b.BackupID, "last_run", database.Now())
	s.setStatus(b.BackupID, "last_error", "")
	s.applyRetention(b)
	s.log.Audit("", userID, b.ServerID, "", fmt.Sprintf("Ran backup %q (%s)", b.BackupName, b.BackupType))
	s.bus.Publish(events.Event{Type: events.EvBackupDone, ServerID: b.ServerID,
		Data: map[string]interface{}{"ok": true, "backup_id": backupID, "name": sup.Definition().ServerName}})
	s.bus.Publish(events.Event{Type: events.EvServerBackup, ServerID: b.ServerID,
		Data: map[string]interface{}{"server_id": b.ServerID, "name": sup.Definition().ServerName}})

	if b.Shutdown == 1 && wasRunning {
		go func() {
			time.Sleep(2 * time.Second)
			lock.Release()
			_ = sup.Start(userID)
		}()
	}
	return nil
}

func (s *Service) setStatus(backupID, key string, value interface{}) {
	b, err := s.db.GetBackup(backupID)
	if err != nil {
		return
	}
	st := map[string]interface{}{}
	if b.Status != nil && *b.Status != "" {
		json.Unmarshal([]byte(*b.Status), &st)
	}
	st[key] = value
	if t, ok := value.(string); ok && t == "" {
		delete(st, key)
	}
	raw, _ := json.Marshal(st)
	str := string(raw)
	s.db.SetBackupStatus(&str, backupID)
}

// runZipVault creates <location>/<server-id>_<timestamp>.zip excluding excludedDirs.
func (s *Service) runZipVault(b gen.Backup, def gen.Server) error {
	stamp := time.Now().UTC().Format("2006-01-02_15-04-05")
	name := fmt.Sprintf("%s_%s.zip", def.ServerID, stamp)
	outPath := filepath.Join(b.BackupLocation, name)
	excludes := splitCSV(b.ExcludedDirs)

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()

	root := def.Path
	return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		for _, ex := range excludes {
			if matched, _ := filepath.Match(ex, rel); matched {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(rel, ex+string(os.PathSeparator)) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if info.IsDir() {
			return nil
		}
		return addZipEntry(zw, p, rel)
	})
}

// snapshot manifest entry.
type snapEntry struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
	Size int64  `json:"size"`
	Mode uint32 `json:"mode"`
}

// runSnapshot stores files by content hash under <location>/repo/<aa>/<hash>
// and writes a manifest at <location>/<server-id>_<timestamp>.snap.json.
func (s *Service) runSnapshot(b gen.Backup, def gen.Server) error {
	stamp := time.Now().UTC().Format("2006-01-02_15-04-05")
	repoDir := filepath.Join(b.BackupLocation, "repo")
	manifestPath := filepath.Join(b.BackupLocation, fmt.Sprintf("%s_%s.snap.json", def.ServerID, stamp))
	excludes := splitCSV(b.ExcludedDirs)

	entries := []snapEntry{}
	root := def.Path
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		for _, ex := range excludes {
			if matched, _ := filepath.Match(ex, rel); matched || strings.HasPrefix(rel, ex+string(os.PathSeparator)) {
				return nil
			}
		}
		hash, size, err := storeByHash(p, repoDir)
		if err != nil {
			return nil
		}
		entries = append(entries, snapEntry{Path: filepath.ToSlash(rel), Hash: hash, Size: size, Mode: uint32(info.Mode().Perm())})
		return nil
	})
	if err != nil {
		return err
	}
	mf := map[string]interface{}{
		"server_id": def.ServerID,
		"created":   database.Now(),
		"entries":   entries,
		"version":   1,
	}
	data, _ := json.MarshalIndent(mf, "", "  ")
	return os.WriteFile(manifestPath, data, 0o644)
}

func storeByHash(src, repoDir string) (hash string, size int64, err error) {
	in, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	h := sha256.New()
	st, _ := in.Stat()
	if _, err := io.Copy(h, in); err != nil {
		return "", 0, err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	os.MkdirAll(filepath.Join(repoDir, digest[:2]), 0o755)
	blob := filepath.Join(repoDir, digest[:2], digest)
	if _, err := os.Stat(blob); err == nil {
		return digest, st.Size(), nil // dedup
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	out, err := os.Create(blob)
	if err != nil {
		return "", 0, err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return "", 0, err
	}
	return digest, st.Size(), nil
}

func addZipEntry(zw *zip.Writer, path, rel string) error {
	in, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer in.Close()
	w, err := zw.Create(filepath.ToSlash(rel))
	if err != nil {
		return err
	}
	_, err = io.Copy(w, in)
	return err
}

// applyRetention prunes oldest archives beyond max_backups (0 = keep all).
func (s *Service) applyRetention(b gen.Backup) {
	if b.MaxBackups <= 0 {
		return
	}
	entries, err := os.ReadDir(b.BackupLocation)
	if err != nil {
		return
	}
	var archives []string
	prefix := ""
	if def, err := s.db.GetServer(b.ServerID); err == nil {
		prefix = def.ServerID + "_"
	}
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, prefix) && (strings.HasSuffix(n, ".zip") || strings.HasSuffix(n, ".snap.json")) {
			archives = append(archives, n)
		}
	}
	sort.Strings(archives) // timestamped names sort chronologically
	for len(archives) > int(b.MaxBackups) {
		oldest := archives[0]
		archives = archives[1:]
		os.Remove(filepath.Join(b.BackupLocation, oldest))
		s.log.Get("backup").Info("retention: removed %s", oldest)
	}
}

// Archives lists executed backups (files) for a config.
type ArchiveFile struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Path     string    `json:"path"`
	Type     string    `json:"type"` // zip | snapshot
}

// Archives scans the backup location for artifacts.
func (s *Service) Archives(backupID string) ([]ArchiveFile, error) {
	b, err := s.db.GetBackup(backupID)
	if err != nil {
		return nil, ErrNotFound
	}
	out := []ArchiveFile{}
	entries, err := os.ReadDir(b.BackupLocation)
	if err != nil {
		return out, nil
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		t := "zip"
		if strings.HasSuffix(e.Name(), ".snap.json") {
			t = "snapshot"
		}
		out = append(out, ArchiveFile{Name: e.Name(), Size: info.Size(), Modified: info.ModTime().UTC(),
			Path: filepath.Join(b.BackupLocation, e.Name()), Type: t})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// ListArchiveFiles enumerates entries inside a zip archive.
func (s *Service) ListArchiveFiles(path string) ([]string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	out := []string{}
	for _, f := range zr.File {
		out = append(out, f.Name)
	}
	return out, nil
}

// ExtractArchiveFile extracts a single member from a zip archive.
func (s *Service) ExtractArchiveFile(path, member, destPath string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != member {
			continue
		}
		if strings.Contains(filepath.Clean(member), "..") {
			return files.ErrTraversal
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		os.MkdirAll(filepath.Dir(destPath), 0o755)
		out, err := os.Create(destPath)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, rc)
		return err
	}
	return fmt.Errorf("member %q not found", member)
}

// Restore restores a backup into the server directory (stops the server first).
func (s *Service) Restore(backupID, archivePath string, userID int64) error {
	b, err := s.db.GetBackup(backupID)
	if err != nil {
		return ErrNotFound
	}
	sup, err := s.reg.Get(b.ServerID)
	if err != nil {
		return err
	}
	def := sup.Definition()
	wasRunning := sup.State() == "running"
	if wasRunning {
		if err := sup.Stop("restore", userID); err != nil && err != servers.ErrServerNotRunning {
			return err
		}
	}
	if strings.HasSuffix(archivePath, ".snap.json") {
		err = s.restoreSnapshot(archivePath, def.Path)
	} else {
		err = s.restoreZip(archivePath, def.Path)
	}
	if wasRunning && err == nil {
		time.Sleep(2 * time.Second)
		_ = sup.Start(userID)
	}
	if err == nil {
		s.log.Audit("", userID, b.ServerID, "", fmt.Sprintf("Restored backup %s", filepath.Base(archivePath)))
	}
	return err
}

func (s *Service) restoreZip(zipPath, destDir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		name := filepath.Clean(f.Name)
		if strings.Contains(name, "..") {
			continue
		}
		target := filepath.Join(destDir, name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(target), 0o755)
		rc, err := f.Open()
		if err != nil {
			continue
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			continue
		}
		io.Copy(out, rc)
		out.Close()
		rc.Close()
	}
	return nil
}

func (s *Service) restoreSnapshot(manifestPath, destDir string) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var mf struct {
		Entries []snapEntry `json:"entries"`
	}
	if err := json.Unmarshal(data, &mf); err != nil {
		return err
	}
	repoDir := filepath.Join(filepath.Dir(manifestPath), "repo")
	for _, e := range mf.Entries {
		if strings.Contains(filepath.Clean(e.Path), "..") {
			continue
		}
		blob := filepath.Join(repoDir, e.Hash[:2], e.Hash)
		target := filepath.Join(destDir, filepath.FromSlash(e.Path))
		os.MkdirAll(filepath.Dir(target), 0o755)
		in, err := os.Open(blob)
		if err != nil {
			continue
		}
		out, err := os.Create(target)
		if err != nil {
			in.Close()
			continue
		}
		io.Copy(out, in)
		out.Close()
		in.Close()
		os.Chmod(target, os.FileMode(e.Mode))
	}
	return nil
}

// DefaultBackup returns the default (primary) config for a server.
func (s *Service) DefaultBackup(serverID string) (gen.Backup, error) {
	return s.db.GetDefaultBackup(serverID)
}

// EnsureDefault creates a default zip_vault config if none exists.
func (s *Service) EnsureDefault(def gen.Server, backupRoot string) error {
	if _, err := s.db.GetDefaultBackup(def.ServerID); err == nil {
		return nil
	}
	loc := filepath.Join(backupRoot, servers.SanitizeServerName(def.ServerName))
	excluded := ".servy,server.properties"
	_, err := s.Create(gen.Backup{
		BackupName: "Default Backup", BackupLocation: loc, ExcludedDirs: &excluded,
		MaxBackups: 0, ServerID: def.ServerID, Compress: 1, Shutdown: 0,
		Default: 1, Enabled: 1, BackupType: "zip_vault",
	})
	return err
}

func splitCSV(s *string) []string {
	if s == nil || *s == "" {
		return nil
	}
	out := []string{}
	for _, p := range strings.Split(*s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
