package backups

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/FlameInTheDark/servy/internal/repo/gen"
)

func strptr(s string) *string { return &s }

func TestBackupScopeWholeServerWhenNoFoldersSelected(t *testing.T) {
	scope := newBackupScope(gen.Backup{})
	for _, rel := range []string{"saves/world/level.dat", "plugins/essentials/config.yml", "server.jar"} {
		if !scope.picks(rel) {
			t.Errorf("picks(%q) = false without an include list", rel)
		}
		if !scope.covers(filepath.Dir(rel)) {
			t.Errorf("covers(%q) = false without an include list", filepath.Dir(rel))
		}
	}
}

func TestBackupScopeRestrictsAndPrunes(t *testing.T) {
	scope := newBackupScope(gen.Backup{
		IncludedDirs: strptr(`saves\world, server.jar`), // a Windows-style list must match too
		ExcludedDirs: strptr("saves/world/tmp"),
	})

	covered := map[string]bool{ // directories the walk descends into
		"saves": true, "saves/world": true, "saves/players": false,
		"plugins": false, "logs": false,
	}
	for dir, want := range covered {
		if got := scope.covers(dir); got != want {
			t.Errorf("covers(%q) = %v, want %v", dir, got, want)
		}
	}

	picked := map[string]bool{ // files the walk keeps (exclusions are applied on top)
		"saves/world/level.dat":         true,
		"saves/world/tmp/level.dat":     true,  // inside an included folder...
		"saves/players/steve.dat":       false, // sibling folder, not selected
		"plugins/essentials/config.yml": false,
		"server.jar":                    true,
	}
	for rel, want := range picked {
		if got := scope.picks(rel); got != want {
			t.Errorf("picks(%q) = %v, want %v", rel, got, want)
		}
	}
	// ...but the walkers drop it because the user excluded that folder
	for _, rel := range []string{"saves/world/tmp", "saves/world/tmp/level.dat"} {
		if !scope.skips(rel) {
			t.Errorf("skips(%q) = false, want true", rel)
		}
	}
	if scope.skips("saves/world/level.dat") {
		t.Error("skips(saves/world/level.dat) = true, want false")
	}
}

// serverTree lays out a small server directory and returns its root.
func serverTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := []string{
		"saves/world/level.dat",
		"saves/players/steve.dat",
		"plugins/essentials/config.yml",
		"server.jar",
	}
	for _, rel := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestZipVaultBacksUpSelectedFoldersOnly(t *testing.T) {
	root := serverTree(t)
	location := t.TempDir()
	svc := &Service{}
	backup := gen.Backup{
		BackupLocation: location,
		IncludedDirs:   strptr("saves, server.jar"),
		ExcludedDirs:   strptr("saves/players"),
	}
	if err := svc.runZipVault(backup, gen.Server{ServerID: "srv1", Path: root}); err != nil {
		t.Fatalf("runZipVault: %v", err)
	}
	archives, err := filepath.Glob(filepath.Join(location, "srv1_*.zip"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("archives = %v (err %v), want exactly one", archives, err)
	}
	zr, err := zip.OpenReader(archives[0])
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	want := []string{"saves/world/level.dat", "server.jar"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("archive contains %v, want %v", names, want)
	}
}

func TestZipVaultWithoutSelectionKeepsWholeServer(t *testing.T) {
	root := serverTree(t)
	location := t.TempDir()
	svc := &Service{}
	backup := gen.Backup{BackupLocation: location, ExcludedDirs: strptr("plugins")}
	if err := svc.runZipVault(backup, gen.Server{ServerID: "srv2", Path: root}); err != nil {
		t.Fatalf("runZipVault: %v", err)
	}
	archives, _ := filepath.Glob(filepath.Join(location, "srv2_*.zip"))
	if len(archives) != 1 {
		t.Fatalf("archives = %v, want exactly one", archives)
	}
	zr, err := zip.OpenReader(archives[0])
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer zr.Close()
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"saves/world/level.dat", "saves/players/steve.dat", "server.jar"} {
		if !names[want] {
			t.Errorf("archive is missing %q: %v", want, names)
		}
	}
	if names["plugins/essentials/config.yml"] {
		t.Errorf("excluded folder leaked into the archive: %v", names)
	}
}

func TestSnapshotBacksUpSelectedFoldersOnly(t *testing.T) {
	root := serverTree(t)
	location := t.TempDir()
	svc := &Service{}
	backup := gen.Backup{
		BackupLocation: location,
		IncludedDirs:   strptr("saves/world"),
	}
	if err := svc.runSnapshot(backup, gen.Server{ServerID: "srv3", Path: root}); err != nil {
		t.Fatalf("runSnapshot: %v", err)
	}
	manifests, _ := filepath.Glob(filepath.Join(location, "srv3_*.snap.json"))
	if len(manifests) != 1 {
		t.Fatalf("manifests = %v, want exactly one", manifests)
	}
	data, err := os.ReadFile(manifests[0])
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Entries []struct {
			Path string `json:"path"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	paths := []string{}
	for _, e := range manifest.Entries {
		paths = append(paths, e.Path)
	}
	sort.Strings(paths)
	if want := []string{"saves/world/level.dat"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("manifest lists %v, want %v", paths, want)
	}
}
