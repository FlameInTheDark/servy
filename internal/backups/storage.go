package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/FlameInTheDark/servy/internal/backupstore"
	"github.com/FlameInTheDark/servy/internal/database"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
)

// ErrStorageInUse is returned when a destination is still referenced by a
// backup config and the caller did not ask for a forced delete.
var ErrStorageInUse = errors.New("STORAGE_IN_USE")

// maskedSecret replaces stored secrets in API responses. Sending it back means
// "keep what is stored".
const maskedSecret = "********"

// StorageView is the API shape of a storage destination.
type StorageView struct {
	StorageID string                `json:"storage_id"`
	Created   string                `json:"created"`
	Name      string                `json:"name"`
	Kind      string                `json:"kind"`
	Prefix    string                `json:"prefix"`
	Settings  map[string]string     `json:"settings"`
	Secrets   map[string]bool       `json:"secrets"`
	Enabled   bool                  `json:"enabled"`
	Default   bool                  `json:"default"`
	ClusterID string                `json:"cluster_id"`
	Location  string                `json:"location,omitempty"`
	Namespace string                `json:"namespace,omitempty"`
	Fields    []backupstore.Setting `json:"fields"`
	UsedBy    []StorageUsage        `json:"used_by,omitempty"`
}

// StorageKindView describes a backend the panel offers to the UI. The form is
// rendered from Fields, so a new backend needs no frontend change.
type StorageKindView struct {
	Kind   string                `json:"kind"`
	Label  string                `json:"label"`
	Fields []backupstore.Setting `json:"fields"`
}

// AdvertisedKinds lists the backends shown in the picker. SFTP works (see
// internal/backupstore) but is left out of the menu for now — FTP/FTPS covers
// the same ground for the setups we ship.
func AdvertisedKinds() []StorageKindView {
	kinds := []StorageKindView{
		{Kind: backupstore.KindS3, Label: "S3-compatible (AWS, MinIO, R2, B2, Spaces, Wasabi)"},
		{Kind: backupstore.KindFTP, Label: "FTP / FTPS"},
		{Kind: backupstore.KindWebDAV, Label: "WebDAV (Nextcloud, ownCloud, …)"},
	}
	for i := range kinds {
		kinds[i].Fields = backupstore.Settings(kinds[i].Kind)
	}
	return kinds
}

// StorageUsage names a backup config that uploads to a destination.
type StorageUsage struct {
	BackupID   string `json:"backup_id"`
	BackupName string `json:"backup_name"`
	ServerID   string `json:"server_id"`
}

// StoragePlan is everything a backup run needs to reach its destination. The
// panel builds it and ships it to a node inside the backup bundle, so the node
// never needs the panel's configuration.
type StoragePlan struct {
	Destination *backupstore.Destination `json:"destination,omitempty"`
	KeepLocal   int                      `json:"keep_local"`
	KeepRemote  int                      `json:"keep_remote"`
}

// Active reports whether the plan uploads somewhere.
func (p StoragePlan) Active() bool { return p.Destination != nil }

// namespace is the per-cluster part of every object key.
func (p StoragePlan) namespace() string {
	if p.Destination == nil {
		return ""
	}
	return backupstore.ClusterPrefix(p.Destination.ClusterID)
}

// decodeSettings parses the stored JSON settings of a destination row.
func decodeSettings(raw string) map[string]string {
	out := map[string]string{}
	if raw == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]string{}
	}
	return out
}

// DestinationFromRow builds a backupstore destination from a database row.
func DestinationFromRow(row gen.BackupStorage) backupstore.Destination {
	return backupstore.Destination{
		ID:        row.StorageID,
		Name:      row.Name,
		Kind:      row.Kind,
		Prefix:    row.Prefix,
		Settings:  decodeSettings(row.Settings),
		Enabled:   row.Enabled == 1,
		Default:   row.Default == 1,
		ClusterID: row.ClusterID,
	}
}

// storageRowView converts a database row into the API shape, masking secrets.
func storageRowView(row gen.BackupStorage, usedBy []StorageUsage) StorageView {
	settings := decodeSettings(row.Settings)
	secrets := map[string]bool{}
	for key, value := range settings {
		if backupstore.IsSecret(key) {
			if value != "" {
				secrets[key] = true
			}
			settings[key] = maskedSecret
		}
	}
	dest := DestinationFromRow(row)
	return StorageView{
		StorageID: row.StorageID, Created: row.Created, Name: row.Name, Kind: row.Kind,
		Prefix: row.Prefix, Settings: settings, Secrets: secrets,
		Enabled: row.Enabled == 1, Default: row.Default == 1, ClusterID: row.ClusterID,
		Fields:    backupstore.Settings(row.Kind),
		UsedBy:    usedBy,
		Location:  dest.DisplayLocation(),
		Namespace: dest.Namespace(),
	}
}

// ListStorages returns every configured destination.
func (s *Service) ListStorages() []StorageView {
	rows, err := s.db.ListBackupStorages()
	if err != nil {
		return []StorageView{}
	}
	out := []StorageView{}
	for _, row := range rows {
		out = append(out, storageRowView(row, s.storageUsage(row.StorageID)))
	}
	return out
}

// GetStorageView loads one destination.
func (s *Service) GetStorageView(storageID string) (StorageView, error) {
	row, err := s.db.GetBackupStorage(storageID)
	if err != nil {
		return StorageView{}, ErrNotFound
	}
	return storageRowView(row, s.storageUsage(storageID)), nil
}

// storageUsage lists the backup configs pointing at a destination.
func (s *Service) storageUsage(storageID string) []StorageUsage {
	rows, err := s.db.ListBackupsForStorage(storageID)
	if err != nil {
		return nil
	}
	out := []StorageUsage{}
	for _, r := range rows {
		out = append(out, StorageUsage{BackupID: r.BackupID, BackupName: r.BackupName, ServerID: r.ServerID})
	}
	return out
}

// StorageInput is a create/update request for a destination.
type StorageInput struct {
	Name     string            `json:"name"`
	Kind     string            `json:"kind"`
	Prefix   string            `json:"prefix"`
	Settings map[string]string `json:"settings"`
	Enabled  bool              `json:"enabled"`
	Default  bool              `json:"default"`
}

// mergeSettings validates incoming settings against the stored ones. An empty
// or masked value keeps what is stored, so the UI never has to resend secrets
// and a "test" without a body checks the saved configuration as-is.
func mergeSettings(kind string, incoming, stored map[string]string) (map[string]string, error) {
	for key := range incoming {
		if !backupstore.KnownSetting(kind, key) {
			return nil, fmt.Errorf("unknown setting %q for %s storage", key, kind)
		}
	}
	merged := map[string]string{}
	for _, field := range backupstore.Settings(kind) {
		value := strings.TrimSpace(incoming[field.Key])
		if value == "" || (backupstore.IsSecret(field.Key) && value == maskedSecret) {
			if old := stored[field.Key]; old != "" {
				merged[field.Key] = old
				continue
			}
			if field.Default != "" {
				merged[field.Key] = field.Default
			}
			continue
		}
		merged[field.Key] = value
	}
	if err := backupstore.Validate(kind, merged); err != nil {
		return nil, err
	}
	return merged, nil
}

// CreateStorage adds a destination.
func (s *Service) CreateStorage(in StorageInput) (StorageView, error) {
	if strings.TrimSpace(in.Name) == "" {
		return StorageView{}, errors.New("name is required")
	}
	if !backupstore.ValidKind(in.Kind) {
		return StorageView{}, fmt.Errorf("unknown storage type %q", in.Kind)
	}
	settings, err := mergeSettings(in.Kind, in.Settings, nil)
	if err != nil {
		return StorageView{}, err
	}
	raw, _ := json.Marshal(settings)
	if in.Default {
		_ = s.db.UnsetDefaultBackupStorages()
	}
	row, err := s.db.CreateBackupStorage(newID(), database.Now(), strings.TrimSpace(in.Name), in.Kind,
		strings.TrimSpace(in.Prefix), string(raw), boolToInt(in.Enabled), boolToInt(in.Default), s.panelClusterID())
	if err != nil {
		return StorageView{}, err
	}
	return storageRowView(row, nil), nil
}

// UpdateStorage changes a destination. Secrets left empty keep their value.
func (s *Service) UpdateStorage(storageID string, in StorageInput) (StorageView, error) {
	row, err := s.db.GetBackupStorage(storageID)
	if err != nil {
		return StorageView{}, ErrNotFound
	}
	kind := in.Kind
	if kind == "" {
		kind = row.Kind
	}
	if !backupstore.ValidKind(kind) {
		return StorageView{}, fmt.Errorf("unknown storage type %q", kind)
	}
	stored := decodeSettings(row.Settings)
	if kind != row.Kind {
		stored = nil // switching kinds cannot carry settings over
	}
	settings, err := mergeSettings(kind, in.Settings, stored)
	if err != nil {
		return StorageView{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = row.Name
	}
	if in.Default {
		_ = s.db.UnsetDefaultBackupStorages()
	}
	raw, _ := json.Marshal(settings)
	clusterID := row.ClusterID
	if clusterID == "" {
		clusterID = s.panelClusterID()
	}
	if err := s.db.UpdateBackupStorage(name, kind, strings.TrimSpace(in.Prefix), string(raw),
		boolToInt(in.Enabled), boolToInt(in.Default), clusterID, storageID); err != nil {
		return StorageView{}, err
	}
	return s.GetStorageView(storageID)
}

// DeleteStorage removes a destination. Backup configs pointing at it are
// detached (they keep their archives local) when force is set.
func (s *Service) DeleteStorage(storageID string, force bool) error {
	if _, err := s.db.GetBackupStorage(storageID); err != nil {
		return ErrNotFound
	}
	usage := s.storageUsage(storageID)
	if len(usage) > 0 && !force {
		return ErrStorageInUse
	}
	for _, item := range usage {
		b, err := s.db.GetBackup(item.BackupID)
		if err != nil {
			continue
		}
		if err := s.db.UpdateBackup(b.BackupName, b.BackupLocation, b.ExcludedDirs, b.MaxBackups,
			b.Compress, b.Shutdown, b.Before, b.After, b.Default, b.Status, b.Enabled, b.BackupType,
			"", item.BackupID); err != nil {
			return err
		}
	}
	return s.db.DeleteBackupStorage(storageID)
}

// TestStorage opens a destination and pings it. Saved secrets are reused when
// the request only carries the masked placeholder.
func (s *Service) TestStorage(storageID string, in StorageInput) error {
	kind := in.Kind
	stored := map[string]string{}
	if storageID != "" {
		row, err := s.db.GetBackupStorage(storageID)
		if err != nil {
			return ErrNotFound
		}
		stored = decodeSettings(row.Settings)
		if kind == "" {
			kind = row.Kind
		}
		if in.Prefix == "" {
			in.Prefix = row.Prefix
		}
		if kind != row.Kind {
			stored = nil
		}
	}
	settings, err := mergeSettings(kind, in.Settings, stored)
	if err != nil {
		return err
	}
	clusterID := s.panelClusterID()
	if storageID != "" {
		if row, err := s.db.GetBackupStorage(storageID); err == nil && row.ClusterID != "" {
			clusterID = row.ClusterID
		}
	}
	dest := backupstore.Destination{Kind: kind, Prefix: in.Prefix, Settings: settings, ClusterID: clusterID}
	store, err := backupstore.Open(dest)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		return err
	}
	// Prove the namespace below the cluster prefix is writable as well.
	probe := backupstore.Key(backupstore.ClusterPrefix(clusterID), "connection-test", "probe.txt")
	if err := store.Put(ctx, probe, strings.NewReader("servy"), 5); err != nil {
		return err
	}
	_ = store.Delete(ctx, probe)
	return nil
}

// DefaultStorage returns the destination a new backup config should use.
func (s *Service) DefaultStorage() *backupstore.Destination {
	row, err := s.db.GetDefaultBackupStorage()
	if err != nil {
		return nil
	}
	dest := DestinationFromRow(row)
	return &dest
}

// storageFor loads the destination a backup config points at.
func (s *Service) storageFor(b gen.Backup) *backupstore.Destination {
	if b.StorageID == "" {
		return nil
	}
	row, err := s.db.GetBackupStorage(b.StorageID)
	if err != nil {
		return nil
	}
	dest := DestinationFromRow(row)
	if !dest.Enabled {
		return nil
	}
	if dest.ClusterID == "" {
		dest.ClusterID = s.panelClusterID()
	}
	return &dest
}

// PlanFor builds the storage plan of a backup config on the panel. A disabled
// destination leaves the plan local-only, so archives keep working locally.
func (s *Service) PlanFor(b gen.Backup) StoragePlan {
	plan := StoragePlan{Destination: s.storageFor(b)}
	if s.cfg != nil {
		cfg := s.cfg.Get()
		plan.KeepLocal = cfg.BackupLocalKeep
		plan.KeepRemote = cfg.BackupRemoteKeep
	}
	return plan
}

// panelClusterID is the namespace segment of this panel.
func (s *Service) panelClusterID() string {
	if s.cfg == nil {
		return ""
	}
	return s.cfg.Get().ClusterID
}

/* ------------------------------ remote objects ----------------------------- */

// remoteKey is the object key of an archive inside the destination namespace.
func remoteKey(plan StoragePlan, serverID, name string) string {
	return backupstore.Key(plan.namespace(), serverID, name)
}

// openStore opens the store a plan points at.
func (s *Service) openStore(plan StoragePlan) (backupstore.Store, error) {
	if plan.Destination == nil {
		return nil, errors.New("no storage destination configured")
	}
	return backupstore.Open(*plan.Destination)
}

// UploadArchive copies one local archive file to the destination.
func (s *Service) UploadArchive(plan StoragePlan, serverID, archivePath string) error {
	if plan.Destination == nil {
		return errors.New("no storage destination configured")
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		return err
	}
	store, err := s.openStore(plan)
	if err != nil {
		return err
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout(info.Size()))
	defer cancel()
	return store.Put(ctx, remoteKey(plan, serverID, filepath.Base(archivePath)), f, info.Size())
}

// uploadTimeout scales with the archive size (slow uplinks need more than the
// default of an HTTP client).
func uploadTimeout(size int64) time.Duration {
	minutes := 10 + int(size/(4<<20)) // ~4 MiB per minute plus a generous base
	if minutes > 720 {
		minutes = 720
	}
	return time.Duration(minutes) * time.Minute
}

// remoteTimeout bounds the shorter remote operations.
func remoteTimeout() time.Duration { return 10 * time.Minute }

// RemoteArchives lists the archives a destination holds for a server.
func (s *Service) RemoteArchives(plan StoragePlan, serverID string) ([]ArchiveFile, error) {
	if plan.Destination == nil {
		return nil, nil
	}
	store, err := s.openStore(plan)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout())
	defer cancel()
	prefix := backupstore.PrefixFor(plan.namespace(), serverID)
	objects, err := store.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	out := []ArchiveFile{}
	for _, obj := range objects {
		name := obj.Key
		if idx := strings.LastIndex(name, "/"); idx >= 0 {
			name = name[idx+1:]
		}
		if name == "" {
			continue
		}
		out = append(out, ArchiveFile{
			Name: name, Size: obj.Size, Modified: obj.Modified.UTC(),
			Type: archiveType(name), Source: "remote",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// DeleteRemoteArchive removes one archive from the destination.
func (s *Service) DeleteRemoteArchive(plan StoragePlan, serverID, name string) error {
	if plan.Destination == nil {
		return nil
	}
	store, err := s.openStore(plan)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout())
	defer cancel()
	return store.Delete(ctx, remoteKey(plan, serverID, name))
}

// DeleteRemoteServer removes every archive of a server from the destination.
func (s *Service) DeleteRemoteServer(plan StoragePlan, serverID string) error {
	if plan.Destination == nil {
		return nil
	}
	store, err := s.openStore(plan)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout())
	defer cancel()
	objects, err := store.List(ctx, backupstore.PrefixFor(plan.namespace(), serverID))
	if err != nil {
		return err
	}
	for _, obj := range objects {
		if err := store.Delete(ctx, obj.Key); err != nil {
			return err
		}
	}
	return nil
}

// FetchRemoteArchive downloads an archive into dir and returns its local path.
func (s *Service) FetchRemoteArchive(plan StoragePlan, serverID, name, dir string) (string, error) {
	if plan.Destination == nil {
		return "", errors.New("no storage destination configured")
	}
	store, err := s.openStore(plan)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
	defer cancel()
	rc, err := store.Get(ctx, remoteKey(plan, serverID, name))
	if err != nil {
		return "", err
	}
	defer rc.Close()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(dir, filepath.Base(name))
	tmp := target + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return target, nil
}

// PruneRemote keeps the newest keep archives of a server in the destination.
// keep < 0 is treated as "keep everything".
func (s *Service) PruneRemote(plan StoragePlan, serverID string, keep int) (int, error) {
	if plan.Destination == nil || keep < 0 {
		return 0, nil
	}
	archives, err := s.RemoteArchives(plan, serverID)
	if err != nil {
		return 0, err
	}
	// Timestamped names sort chronologically, like the local files do.
	names := make([]string, 0, len(archives))
	for _, a := range archives {
		names = append(names, a.Name)
	}
	sort.Strings(names)
	removed := 0
	for len(names) > keep {
		oldest := names[0]
		names = names[1:]
		if err := s.DeleteRemoteArchive(plan, serverID, oldest); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// newID returns a fresh identifier for a destination row.
func newID() string { return uuid.NewString() }

func boolToInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

/* --------------------------------- bundle --------------------------------- */

// Bundle is what the panel ships to a node for a backup operation: the backup
// row plus the storage plan (destination, per-cluster namespace and the
// retention counts). The node therefore never needs the panel's configuration.
type Bundle struct {
	Backup gen.Backup  `json:"backup"`
	Plan   StoragePlan `json:"plan"`
}

// BundleFor builds the payload of a backup operation on the panel.
func (s *Service) BundleFor(b gen.Backup) Bundle {
	return Bundle{Backup: b, Plan: s.PlanFor(b)}
}

// EncodeBundle renders a bundle for the wire.
func EncodeBundle(b Bundle) (string, error) {
	raw, err := json.Marshal(b)
	return string(raw), err
}

// DecodeBundle parses a bundle. Panels older than the storage feature sent a
// bare gen.Backup row, which is still accepted.
func DecodeBundle(payload string) (Bundle, error) {
	var bundle Bundle
	if strings.TrimSpace(payload) == "" {
		return bundle, errors.New("empty backup bundle")
	}
	if err := json.Unmarshal([]byte(payload), &bundle); err != nil {
		return bundle, fmt.Errorf("invalid backup bundle: %w", err)
	}
	if bundle.Backup.BackupID == "" {
		var row gen.Backup
		if err := json.Unmarshal([]byte(payload), &row); err == nil && row.BackupID != "" {
			return Bundle{Backup: row}, nil
		}
	}
	if bundle.Backup.BackupID == "" {
		return bundle, errors.New("backup id required")
	}
	return bundle, nil
}
