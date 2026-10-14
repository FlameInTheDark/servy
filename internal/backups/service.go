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

	"github.com/FlameInTheDark/servy/internal/backupstore"
	"github.com/FlameInTheDark/servy/internal/config"
	"github.com/FlameInTheDark/servy/internal/database"
	"github.com/FlameInTheDark/servy/internal/events"
	"github.com/FlameInTheDark/servy/internal/files"
	"github.com/FlameInTheDark/servy/internal/logging"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
	"github.com/FlameInTheDark/servy/internal/servers"
	"github.com/FlameInTheDark/servy/internal/steamcmd"
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
	cfg *config.Manager
}

// New builds the service.
func New(db *database.Queries, log *logging.Manager, bus *events.Bus, reg *servers.Registry) *Service {
	return &Service{db: db, log: log, bus: bus, reg: reg}
}

// SetConfig hands the service the panel configuration (retention counts and the
// cluster id used as the storage namespace).
func (s *Service) SetConfig(cfg *config.Manager) { s.cfg = cfg }

// ConfigView is the API shape of a backup config.
type ConfigView struct {
	BackupID       string                 `json:"backup_id"`
	Created        string                 `json:"created"`
	BackupName     string                 `json:"backup_name"`
	BackupLocation string                 `json:"backup_location"`
	ExcludedDirs   []string               `json:"excluded_dirs"`
	IncludedDirs   []string               `json:"included_dirs"`
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
	StorageID      string                 `json:"storage_id,omitempty"`
}

// View converts a DB row.
func (s *Service) View(b gen.Backup) ConfigView {
	v := ConfigView{
		BackupID: b.BackupID, Created: b.Created, BackupName: b.BackupName,
		BackupLocation: b.BackupLocation, MaxBackups: b.MaxBackups, ServerID: b.ServerID,
		Compress: b.Compress == 1, Shutdown: b.Shutdown == 1, Default: b.Default == 1,
		Enabled: b.Enabled == 1, BackupType: b.BackupType, StorageID: b.StorageID,
		ExcludedDirs: []string{}, IncludedDirs: []string{},
		Status: map[string]interface{}{},
	}
	v.ExcludedDirs = splitCSV(b.ExcludedDirs)
	v.IncludedDirs = splitCSV(b.IncludedDirs)
	if v.ExcludedDirs == nil {
		v.ExcludedDirs = []string{}
	}
	if v.IncludedDirs == nil {
		v.IncludedDirs = []string{}
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
	return s.db.CreateBackup(b.BackupID, b.Created, b.BackupName, b.BackupLocation, b.ExcludedDirs, b.IncludedDirs,
		b.MaxBackups, b.ServerID, b.Compress, b.Shutdown, b.Before, b.After, b.Default, b.Status, b.Enabled, b.BackupType, b.StorageID)
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
	return s.db.UpdateBackup(b.BackupName, b.BackupLocation, b.ExcludedDirs, b.IncludedDirs, b.MaxBackups, b.Compress,
		b.Shutdown, b.Before, b.After, b.Default, b.Status, b.Enabled, b.BackupType, b.StorageID, b.BackupID)
}

// EnsureConfig mirrors a config row into the local database so this host can
// execute or restore it. Configs are authored on the panel, but archives live
// on the node that owns the server, so the node keeps its own copy of the row
// with a node-local location.
func (s *Service) EnsureConfig(b gen.Backup, defaultLocation string) (gen.Backup, error) {
	if b.BackupID == "" {
		return b, errors.New("backup_id required")
	}
	b.BackupLocation = defaultLocation
	if existing, err := s.db.GetBackup(b.BackupID); err == nil {
		// partial metadata (id + server only) must not wipe stored settings
		b.Created = existing.Created
		if b.ServerID == "" {
			b.ServerID = existing.ServerID
		}
		if b.BackupName == "" {
			b.BackupName = existing.BackupName
		}
		if b.BackupType == "" {
			b.BackupType = existing.BackupType
		}
		if b.ExcludedDirs == nil {
			b.ExcludedDirs = existing.ExcludedDirs
		}
		if b.IncludedDirs == nil {
			b.IncludedDirs = existing.IncludedDirs
		}
		if b.MaxBackups == 0 {
			b.MaxBackups = existing.MaxBackups
		}
		if b.Compress == 0 {
			b.Compress = existing.Compress
		}
		if b.Shutdown == 0 {
			b.Shutdown = existing.Shutdown
		}
		if b.Before == nil {
			b.Before = existing.Before
		}
		if b.After == nil {
			b.After = existing.After
		}
		if b.Default == 0 {
			b.Default = existing.Default
		}
		if b.Status == nil {
			b.Status = existing.Status
		}
		if b.Enabled == 0 {
			b.Enabled = existing.Enabled
		}
		if b.StorageID == "" {
			b.StorageID = existing.StorageID
		}
		if err := s.Update(b); err != nil {
			return b, err
		}
		return s.db.GetBackup(b.BackupID)
	}
	if b.BackupType == "" {
		b.BackupType = "zip_vault"
	}
	if b.Enabled == 0 {
		b.Enabled = 1
	}
	if b.ServerID == "" {
		return b, errors.New("server_id required")
	}
	return s.Create(b)
}

// Delete removes a config (and its files if requested) using the destination
// this panel knows for it.
func (s *Service) Delete(backupID string, removeFiles bool) error {
	b, err := s.db.GetBackup(backupID)
	if err != nil {
		return ErrNotFound
	}
	return s.DeleteWithPlan(backupID, removeFiles, s.PlanFor(b))
}

// DeleteWithPlan is Delete with the destination resolved elsewhere — a node
// that got the plan from the panel does not know the panel's storage rows.
func (s *Service) DeleteWithPlan(backupID string, removeFiles bool, plan StoragePlan) error {
	b, err := s.db.GetBackup(backupID)
	if err != nil {
		return ErrNotFound
	}
	if removeFiles {
		if b.BackupLocation != "" {
			os.RemoveAll(b.BackupLocation)
		}
		if plan.Active() {
			if err := s.DeleteRemoteServer(plan, b.ServerID); err != nil {
				s.log.Get("backup").Warning("removing remote archives of %s failed: %v", b.ServerID, err)
			}
		}
	}
	return s.db.DeleteBackup(backupID)
}

// Run executes a backup now: shutdown option → before cmd → engine → upload to
// the storage destination → retention → restart-if-was-running. Publishes
// backup_start/backup_end.
func (s *Service) Run(backupID string, userID int64) error {
	b, err := s.db.GetBackup(backupID)
	if err != nil {
		return ErrNotFound
	}
	return s.RunWithPlan(backupID, userID, s.PlanFor(b))
}

// RunWithPlan runs a backup with an explicit storage plan. Nodes receive the
// plan from the panel, so they use the panel's destination and retention
// settings instead of their own configuration.
func (s *Service) RunWithPlan(backupID string, userID int64, plan StoragePlan) error {
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
	s.applyRetention(b, plan)
	s.uploadLatest(b, plan)
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
	scope := newBackupScope(b)

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
		if scope.skips(rel) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			if !scope.covers(rel) {
				// no selected folder lives below this directory
				return filepath.SkipDir
			}
			return nil
		}
		if !scope.picks(rel) {
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
	scope := newBackupScope(b)

	entries := []snapEntry{}
	root := def.Path
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		if info.IsDir() {
			if scope.skips(rel) || !scope.covers(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if scope.skips(rel) || !scope.picks(rel) {
			return nil
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

// applyRetention prunes the oldest local archives of a server.
//
// With a storage destination configured the local files are only a cache: the
// panel keeps plan.KeepLocal of them (default 1, so restore stays instant) and
// the destination keeps its own history. Without a destination the per-config
// max_backups applies as before. A keep value of 0 means "keep everything".
func (s *Service) applyRetention(b gen.Backup, plan StoragePlan) {
	keep := int(b.MaxBackups)
	if plan.Active() {
		keep = plan.KeepLocal
	}
	if keep <= 0 {
		return
	}
	entries, err := os.ReadDir(b.BackupLocation)
	if err != nil {
		return
	}
	var archives []string
	prefix := ""
	if b.ServerID != "" {
		prefix = b.ServerID + "_"
	}
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, prefix) && (strings.HasSuffix(n, ".zip") || strings.HasSuffix(n, ".snap.json")) {
			archives = append(archives, n)
		}
	}
	sort.Strings(archives) // timestamped names sort chronologically
	for len(archives) > keep {
		oldest := archives[0]
		archives = archives[1:]
		os.Remove(filepath.Join(b.BackupLocation, oldest))
		s.log.Get("backup").Info("retention: removed %s", oldest)
	}
}

// uploadLatest uploads the newest local archive to the destination and rotates
// the remote history. A failed upload is reported in the config status but does
// not fail the backup itself — the archive stays on disk.
func (s *Service) uploadLatest(b gen.Backup, plan StoragePlan) {
	if !plan.Active() {
		return
	}
	archive, ok := newestArchive(b.BackupLocation, b.ServerID)
	if !ok {
		return
	}
	if err := s.UploadArchive(plan, b.ServerID, archive); err != nil {
		s.log.Get("backup").Error("upload of %s failed: %v", filepath.Base(archive), err)
		s.setStatus(b.BackupID, "last_upload_error", err.Error())
		s.setStatus(b.BackupID, "last_upload", "")
		return
	}
	s.setStatus(b.BackupID, "last_upload", database.Now())
	s.setStatus(b.BackupID, "last_upload_error", "")
	s.setStatus(b.BackupID, "last_upload_archive", filepath.Base(archive))
	if plan.KeepRemote > 0 {
		if removed, err := s.PruneRemote(plan, b.ServerID, plan.KeepRemote); err != nil {
			s.log.Get("backup").Error("remote retention failed: %v", err)
		} else if removed > 0 {
			s.log.Get("backup").Info("remote retention: removed %d archive(s) of %s", removed, b.ServerID)
		}
	}
	// Only now that the archive is safely uploaded may the local cache be
	// trimmed, otherwise a failing destination would cost the history.
	if plan.KeepLocal >= 0 {
		s.pruneLocal(b.BackupLocation, b.ServerID, plan.KeepLocal)
	}
}

// newestArchive returns the most recent archive file of a server.
func newestArchive(dir, serverID string) (string, bool) {
	files, err := ArchivesIn(dir)
	if err != nil || len(files) == 0 {
		return "", false
	}
	for _, f := range files { // ArchivesIn sorts newest first
		if strings.HasPrefix(f.Name, serverID+"_") {
			return f.Path, true
		}
	}
	return "", false
}

// pruneLocal keeps the newest keep archives of a server on disk.
func (s *Service) pruneLocal(dir, serverID string, keep int) int {
	if keep < 0 {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	archives := []string{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, serverID+"_") && (strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".snap.json")) {
			archives = append(archives, name)
		}
	}
	sort.Strings(archives)
	removed := 0
	for len(archives) > keep {
		oldest := archives[0]
		archives = archives[1:]
		if err := os.Remove(filepath.Join(dir, oldest)); err == nil {
			removed++
			s.log.Get("backup").Info("retention: removed local cache %s", oldest)
		}
	}
	return removed
}

// Archives lists executed backups (files) for a config.
type ArchiveFile struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Path     string    `json:"path,omitempty"`
	Type     string    `json:"type"`   // zip | snapshot
	Source   string    `json:"source"` // local | remote | both
}

// archiveType maps an archive name to zip or snapshot.
func archiveType(name string) string {
	if strings.HasSuffix(name, ".snap.json") {
		return "snapshot"
	}
	return "zip"
}

// Archives scans the backup location for artifacts and adds the archives that
// only exist in the storage destination, so the UI can show and restore them.
func (s *Service) Archives(backupID string) ([]ArchiveFile, error) {
	b, err := s.db.GetBackup(backupID)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.ArchivesWithPlan(b, s.PlanFor(b))
}

// ArchivesWithPlan merges local and remote archives of a config.
func (s *Service) ArchivesWithPlan(b gen.Backup, plan StoragePlan) ([]ArchiveFile, error) {
	local, err := ArchivesIn(b.BackupLocation)
	if err != nil {
		local = []ArchiveFile{}
	}
	for i := range local {
		local[i].Source = "local"
	}
	if !plan.Active() {
		return local, nil
	}
	remote, rerr := s.RemoteArchives(plan, b.ServerID)
	if rerr != nil {
		// A destination that is down must not hide the local archives.
		s.log.Get("backup").Warning("listing remote archives of %s failed: %v", b.ServerID, rerr)
		return local, nil
	}
	byName := map[string]int{}
	for i, a := range local {
		byName[a.Name] = i
	}
	for _, a := range remote {
		if idx, ok := byName[a.Name]; ok {
			local[idx].Source = "both"
			if local[idx].Size == 0 {
				local[idx].Size = a.Size
			}
			continue
		}
		local = append(local, a)
	}
	sort.Slice(local, func(i, j int) bool {
		if local[i].Name != local[j].Name {
			return local[i].Name > local[j].Name
		}
		return local[i].Modified.After(local[j].Modified)
	})
	return local, nil
}

// EnsureLocal makes sure an archive is on disk, downloading it from the
// destination when the local cache no longer has it. The returned path is safe
// to restore from.
func (s *Service) EnsureLocal(backupID, name string, plan StoragePlan) (string, error) {
	b, err := s.db.GetBackup(backupID)
	if err != nil {
		return "", ErrNotFound
	}
	localPath := filepath.Join(b.BackupLocation, filepath.Base(name))
	if _, err := os.Stat(localPath); err == nil {
		return localPath, nil
	}
	if !plan.Active() {
		return "", ErrNotFound
	}
	fetched, err := s.FetchRemoteArchive(plan, b.ServerID, filepath.Base(name), b.BackupLocation)
	if err != nil {
		if errors.Is(err, backupstore.ErrNotExist) {
			return "", ErrNotFound
		}
		return "", err
	}
	return fetched, nil
}

// ArchivesIn scans a directory for backup artifacts (used by nodes that store
// the archives of panel-managed servers).
func ArchivesIn(location string) ([]ArchiveFile, error) {
	out := []ArchiveFile{}
	entries, err := os.ReadDir(location)
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
			Path: filepath.Join(location, e.Name()), Type: t})
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
// When the archive is not on disk any more it is fetched from the destination.
func (s *Service) Restore(backupID, archivePath string, userID int64) error {
	b, err := s.db.GetBackup(backupID)
	if err != nil {
		return ErrNotFound
	}
	if _, err := os.Stat(archivePath); err != nil && archivePath == filepath.Join(b.BackupLocation, filepath.Base(archivePath)) {
		plan := s.PlanFor(b)
		if fetched, ferr := s.EnsureLocal(backupID, filepath.Base(archivePath), plan); ferr == nil {
			archivePath = fetched
		}
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
	// The game manifest decides which folders matter (Steam catalog
	// backup_dirs / creation recipe backup_dirs). Without a list the backup
	// keeps covering the whole server directory, like before.
	var included *string
	if dirs := steamcmd.BackupDirsFromDefinitions(def.Definitions); len(dirs) > 0 {
		joined := strings.Join(dirs, ",")
		included = &joined
	}
	_, err := s.Create(gen.Backup{
		BackupName: "Default Backup", BackupLocation: loc, ExcludedDirs: &excluded, IncludedDirs: included,
		MaxBackups: 0, ServerID: def.ServerID, Compress: 1, Shutdown: 0,
		Default: 1, Enabled: 1, BackupType: "zip_vault",
	})
	return err
}

// backupScope decides what one backup picks up. A non-empty included_dirs
// restricts the walk to the folders selected in the server's backup settings
// (pre-filled from the game manifest) and everything below them;
// excluded_dirs subtracts from the result. An empty included_dirs keeps the
// historical behaviour: the whole server directory.
type backupScope struct {
	included []string
	excluded []string
}

func newBackupScope(b gen.Backup) backupScope {
	return backupScope{included: splitCSV(b.IncludedDirs), excluded: splitCSV(b.ExcludedDirs)}
}

// covers reports whether a directory can contain selected folders. When it
// cannot, the walk prunes it instead of descending into gigabytes of game
// files that are not part of the backup.
func (s backupScope) covers(rel string) bool {
	if len(s.included) == 0 {
		return true
	}
	rel = slashPath(rel)
	for _, in := range s.included {
		in = slashPath(in)
		if in == "" {
			continue
		}
		if rel == in || strings.HasPrefix(rel, in+"/") || strings.HasPrefix(in, rel+"/") {
			return true
		}
	}
	return false
}

// picks reports whether a file belongs to the backup.
func (s backupScope) picks(rel string) bool {
	if len(s.included) == 0 {
		return true
	}
	for _, in := range s.included {
		if withinDir(rel, in) {
			return true
		}
	}
	return false
}

// skips reports whether the user removed a path from the scope.
func (s backupScope) skips(rel string) bool {
	for _, ex := range s.excluded {
		if matched, _ := filepath.Match(slashPath(ex), slashPath(rel)); matched {
			return true
		}
		if withinDir(rel, ex) {
			return true
		}
	}
	return false
}

// withinDir reports whether rel is dir itself or lives below it.
func withinDir(rel, dir string) bool {
	rel = slashPath(rel)
	dir = slashPath(dir)
	if dir == "" {
		return false
	}
	return rel == dir || strings.HasPrefix(rel, dir+"/")
}

// slashPath normalizes one folder-list entry for comparison: forward slashes on
// every platform, so a list written on Windows ("saves\world") also matches the
// files of a Linux server, and a stray separator never hides a folder from the
// scope.
func slashPath(p string) string {
	return strings.TrimSuffix(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"), "/")
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
