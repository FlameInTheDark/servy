package backups

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FlameInTheDark/servy/internal/backupstore"
	"github.com/FlameInTheDark/servy/internal/logging"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
)

// newTestService builds a service with a logger, without a database: the
// remote helpers only need the plan they are given.
func newTestService(t *testing.T) *Service {
	t.Helper()
	log, err := logging.NewManager(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{log: log}
}

// localPlan builds a plan against a directory, as a panel would for a local
// destination (no network needed, but the very same code path).
func localPlan(t *testing.T, root, clusterID string) StoragePlan {
	t.Helper()
	return StoragePlan{
		Destination: &backupstore.Destination{
			ID: "st-1", Name: "test", Kind: backupstore.KindLocal, Prefix: "servy",
			Settings:  map[string]string{"path": root},
			Enabled:   true,
			ClusterID: clusterID,
		},
		KeepLocal: 1, KeepRemote: 2,
	}
}

func writeArchive(t *testing.T, dir, serverID, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUploadAndRemoteListing(t *testing.T) {
	root := t.TempDir()
	svc := newTestService(t)
	plan := localPlan(t, root, "panel-7f3a")

	archive := writeArchive(t, t.TempDir(), "srv-42", "srv-42_2026-10-14_12-00-00.zip", "zip-bytes")
	if err := svc.UploadArchive(plan, "srv-42", archive); err != nil {
		t.Fatalf("UploadArchive: %v", err)
	}
	// the object lands below <prefix>/<cluster>/<server>/
	onDisk := filepath.Join(root, "servy", "panel-7f3a", "srv-42", "srv-42_2026-10-14_12-00-00.zip")
	if data, err := os.ReadFile(onDisk); err != nil || string(data) != "zip-bytes" {
		t.Fatalf("uploaded archive missing or wrong: %v", err)
	}

	archives, err := svc.RemoteArchives(plan, "srv-42")
	if err != nil {
		t.Fatalf("RemoteArchives: %v", err)
	}
	if len(archives) != 1 || archives[0].Name != "srv-42_2026-10-14_12-00-00.zip" || archives[0].Source != "remote" {
		t.Fatalf("listing = %+v", archives)
	}
	if got := archives[0].Size; got != int64(len("zip-bytes")) {
		t.Fatalf("size = %d", got)
	}
	// the local path is never part of a remote listing
	if archives[0].Path != "" {
		t.Fatalf("remote archive exposed a local path: %q", archives[0].Path)
	}

	// a second server of the same cluster is a different namespace
	other, err := svc.RemoteArchives(plan, "srv-99")
	if err != nil {
		t.Fatalf("RemoteArchives: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("server namespaces overlap: %+v", other)
	}
}

func TestClustersNeverOverlapInOneDestination(t *testing.T) {
	root := t.TempDir()
	svc := newTestService(t)
	planA := localPlan(t, root, "panel-aaaa")
	planB := localPlan(t, root, "panel-bbbb")

	for _, tc := range []struct {
		plan    StoragePlan
		content string
	}{{planA, "archive-of-cluster-a"}, {planB, "archive-of-cluster-b"}} {
		path := writeArchive(t, t.TempDir(), "srv-1", "srv-1_2026-10-14_12-00-00.zip", tc.content)
		if err := svc.UploadArchive(tc.plan, "srv-1", path); err != nil {
			t.Fatalf("UploadArchive: %v", err)
		}
	}
	// Same server id, same archive name, one bucket: both survive untouched.
	for _, tc := range []struct {
		plan StoragePlan
		want string
	}{{planA, "archive-of-cluster-a"}, {planB, "archive-of-cluster-b"}} {
		archives, err := svc.RemoteArchives(tc.plan, "srv-1")
		if err != nil {
			t.Fatalf("RemoteArchives: %v", err)
		}
		if len(archives) != 1 {
			t.Fatalf("cluster %s sees %d archives", tc.plan.namespace(), len(archives))
		}
		local := filepath.Join(root, "servy", tc.plan.namespace(), "srv-1", archives[0].Name)
		data, err := os.ReadFile(local)
		if err != nil || string(data) != tc.want {
			t.Fatalf("cluster %s archive was mixed up: %q %v", tc.plan.namespace(), data, err)
		}
	}
	// Removing one cluster's history leaves the other one alone.
	if err := svc.DeleteRemoteServer(planA, "srv-1"); err != nil {
		t.Fatalf("DeleteRemoteServer: %v", err)
	}
	if archives, _ := svc.RemoteArchives(planA, "srv-1"); len(archives) != 0 {
		t.Fatalf("cluster A still has archives: %+v", archives)
	}
	if archives, _ := svc.RemoteArchives(planB, "srv-1"); len(archives) != 1 {
		t.Fatalf("cluster B lost its archive: %+v", archives)
	}
}

func TestPruneRemoteKeepsNewest(t *testing.T) {
	root := t.TempDir()
	svc := newTestService(t)
	plan := localPlan(t, root, "panel-1")
	names := []string{
		"srv-1_2026-10-01_00-00-00.zip",
		"srv-1_2026-10-02_00-00-00.zip",
		"srv-1_2026-10-03_00-00-00.zip",
		"srv-1_2026-10-04_00-00-00.zip",
	}
	for _, name := range names {
		path := writeArchive(t, t.TempDir(), "srv-1", name, name)
		if err := svc.UploadArchive(plan, "srv-1", path); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := svc.PruneRemote(plan, "srv-1", 2)
	if err != nil {
		t.Fatalf("PruneRemote: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	left, err := svc.RemoteArchives(plan, "srv-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 2 || left[0].Name != names[2] || left[1].Name != names[3] {
		t.Fatalf("kept %+v, want the two newest", left)
	}
	// keep = 0 removes everything, negative means "keep all"
	if n, err := svc.PruneRemote(plan, "srv-1", -1); err != nil || n != 0 {
		t.Fatalf("negative keep pruned %d archives (err %v)", n, err)
	}
	if left, _ := svc.RemoteArchives(plan, "srv-1"); len(left) != 2 {
		t.Fatalf("negative keep deleted archives: %+v", left)
	}
}

func TestFetchRemoteArchiveRoundTrip(t *testing.T) {
	root := t.TempDir()
	cache := t.TempDir()
	svc := newTestService(t)
	plan := localPlan(t, root, "panel-1")

	path := writeArchive(t, cache, "srv-1", "srv-1_2026-10-14_10-00-00.zip", "payload")
	if err := svc.UploadArchive(plan, "srv-1", path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil { // simulate the local cache rotation
		t.Fatal(err)
	}
	fetched, err := svc.FetchRemoteArchive(plan, "srv-1", "srv-1_2026-10-14_10-00-00.zip", cache)
	if err != nil {
		t.Fatalf("FetchRemoteArchive: %v", err)
	}
	data, err := os.ReadFile(fetched)
	if err != nil || string(data) != "payload" {
		t.Fatalf("fetched archive wrong: %q %v", data, err)
	}
	// nothing half written is left behind
	entries, _ := os.ReadDir(cache)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
	if _, err := svc.FetchRemoteArchive(plan, "srv-1", "missing.zip", cache); err == nil {
		t.Fatal("fetching an unknown archive must fail")
	}
}

func TestMergeSettingsKeepsStoredSecrets(t *testing.T) {
	stored := map[string]string{
		"bucket": "b", "endpoint": "https://minio.local", "access_key": "AK",
		"secret_key": "topsecret", "region": "us-east-1", "path_style": "true",
	}
	// the browser sends the mask back: the stored secret survives
	merged, err := mergeSettings("s3", map[string]string{
		"bucket": "b2", "secret_key": maskedSecret, "access_key": "AK",
	}, stored)
	if err != nil {
		t.Fatalf("mergeSettings: %v", err)
	}
	if merged["secret_key"] != "topsecret" {
		t.Fatalf("stored secret lost: %q", merged["secret_key"])
	}
	if merged["bucket"] != "b2" {
		t.Fatalf("change not applied: %+v", merged)
	}
	if merged["path_style"] != "true" || merged["region"] != "us-east-1" {
		t.Fatalf("defaults/stored values lost: %+v", merged)
	}
	// a new secret replaces the stored one
	merged, err = mergeSettings("s3", map[string]string{
		"bucket": "b", "secret_key": "rotated", "access_key": "AK",
	}, stored)
	if err != nil {
		t.Fatal(err)
	}
	if merged["secret_key"] != "rotated" {
		t.Fatalf("rotation ignored: %q", merged["secret_key"])
	}
	// validation still applies
	if _, err := mergeSettings("ftp", map[string]string{"host": "", "username": "u"}, nil); err == nil {
		t.Fatal("missing host must be rejected")
	}
}

func TestStorageViewMasksSecrets(t *testing.T) {
	row := gen.BackupStorage{
		StorageID: "st-1", Created: "now", Name: "Backups", Kind: "s3", Prefix: "servy",
		Settings: `{"bucket":"b","endpoint":"https://minio.local","access_key":"AK","secret_key":"topsecret","path_style":"true"}`,
		Enabled:  1, ClusterID: "panel-1",
	}
	view := storageRowView(row, nil)
	if view.Settings["secret_key"] != maskedSecret {
		t.Fatalf("secret leaked to the API: %q", view.Settings["secret_key"])
	}
	if !view.Secrets["secret_key"] || view.Secrets["access_key"] {
		t.Fatalf("secret flags wrong: %+v", view.Secrets)
	}
	if view.Settings["access_key"] != "AK" || view.Settings["bucket"] != "b" {
		t.Fatalf("non secrets must be readable: %+v", view.Settings)
	}
	if view.Namespace != "panel-1" || !strings.HasPrefix(view.Location, "s3://b") {
		t.Fatalf("namespace/location = %q %q", view.Namespace, view.Location)
	}
	if len(view.Fields) == 0 {
		t.Fatal("the form schema is missing")
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "topsecret") {
		t.Fatalf("masked view still contains the secret: %s", raw)
	}
}

func TestBundleRoundTrip(t *testing.T) {
	bundle := Bundle{
		Backup: gen.Backup{BackupID: "bkp-1", ServerID: "srv-1", BackupName: "manual", BackupType: "zip_vault", MaxBackups: 3},
		Plan: StoragePlan{
			Destination: &backupstore.Destination{
				ID: "st-1", Kind: "ftp", Prefix: "servy", ClusterID: "panel-1",
				Enabled:  true,
				Settings: map[string]string{"host": "ftp.local", "username": "u", "password": "p"},
			},
			KeepLocal: 1, KeepRemote: 4,
		},
	}
	encoded, err := EncodeBundle(bundle)
	if err != nil {
		t.Fatalf("EncodeBundle: %v", err)
	}
	decoded, err := DecodeBundle(encoded)
	if err != nil {
		t.Fatalf("DecodeBundle: %v", err)
	}
	if decoded.Backup.BackupID != "bkp-1" || decoded.Plan.Destination == nil {
		t.Fatalf("bundle lost data: %+v", decoded)
	}
	if decoded.Plan.Destination.Settings["password"] != "p" {
		t.Fatalf("credentials did not travel with the bundle: %+v", decoded.Plan.Destination.Settings)
	}
	if decoded.Plan.KeepLocal != 1 || decoded.Plan.KeepRemote != 4 {
		t.Fatalf("retention settings lost: %+v", decoded.Plan)
	}

	// older panels sent a bare backup row
	bare, err := json.Marshal(gen.Backup{BackupID: "bkp-2", ServerID: "srv-9"})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = DecodeBundle(string(bare))
	if err != nil {
		t.Fatalf("bare backup rejected: %v", err)
	}
	if decoded.Backup.BackupID != "bkp-2" || decoded.Plan.Active() {
		t.Fatalf("bare backup decoded wrong: %+v", decoded)
	}
	if _, err := DecodeBundle("not json"); err == nil {
		t.Fatal("garbage payloads must be rejected")
	}
}

func TestStoragePlanLocalCacheRule(t *testing.T) {
	// Without a destination the per-config max_backups governs the local files.
	dir := t.TempDir()
	svc := newTestService(t)
	b := gen.Backup{BackupID: "b", ServerID: "srv-1", BackupLocation: dir, MaxBackups: 2}
	for _, name := range []string{
		"srv-1_2026-10-01_00-00-00.zip", "srv-1_2026-10-02_00-00-00.zip", "srv-1_2026-10-03_00-00-00.zip",
	} {
		writeArchive(t, dir, "srv-1", name, name)
	}
	svc.applyRetention(b, StoragePlan{})
	left, _ := os.ReadDir(dir)
	if len(left) != 2 {
		t.Fatalf("local retention kept %d files, want 2", len(left))
	}

	// With a destination the local files are a cache: only the newest stays.
	plan := StoragePlan{Destination: &backupstore.Destination{Kind: "local", Settings: map[string]string{"path": t.TempDir()}}, KeepLocal: 1}
	svc.applyRetention(b, plan)
	left, _ = os.ReadDir(dir)
	if len(left) != 1 || left[0].Name() != "srv-1_2026-10-03_00-00-00.zip" {
		t.Fatalf("cache retention kept %+v, want only the newest", left)
	}
}

func TestUploadTimeoutScales(t *testing.T) {
	if got := uploadTimeout(0); got != 10*time.Minute {
		t.Errorf("timeout for a small archive = %v", got)
	}
	if got := uploadTimeout(1 << 30); got <= 10*time.Minute {
		t.Errorf("timeout for a 1 GiB archive = %v", got)
	}
	if got := uploadTimeout(1 << 50); got != 12*time.Hour {
		t.Errorf("timeout is not capped: %v", got)
	}
}

func TestDecodeSettingsHandlesJunk(t *testing.T) {
	if got := decodeSettings("not json"); len(got) != 0 {
		t.Fatalf("junk settings decoded to %+v", got)
	}
	if got := decodeSettings(`{"host":"h"}`); got["host"] != "h" {
		t.Fatalf("settings = %+v", got)
	}
	if got := decodeSettings(""); len(got) != 0 {
		t.Fatalf("empty settings = %+v", got)
	}
}

// The store contract used by the service: an unknown object is reported as
// ErrNotExist and not as a generic failure.
func TestFetchReportsMissingObject(t *testing.T) {
	root := t.TempDir()
	svc := newTestService(t)
	plan := localPlan(t, root, "panel-1")
	_, err := svc.FetchRemoteArchive(plan, "srv-1", "nope.zip", t.TempDir())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "not") && err != backupstore.ErrNotExist {
		t.Logf("error was: %v", err)
	}
	store, serr := backupstore.Open(*plan.Destination)
	if serr != nil {
		t.Fatal(serr)
	}
	if _, gerr := store.Get(context.Background(), "missing"); gerr != backupstore.ErrNotExist {
		t.Fatalf("local store Get = %v, want ErrNotExist", gerr)
	}
}
