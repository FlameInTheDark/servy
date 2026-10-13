package creation

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlameInTheDark/servy/internal/files"
	"github.com/FlameInTheDark/servy/internal/logging"
)

// newExtractService builds the minimal Service needed by extractArchive.
func newExtractService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	importDir := filepath.Join(root, "import")
	if err := os.MkdirAll(importDir, 0o755); err != nil {
		t.Fatal(err)
	}
	log, err := logging.NewManager(filepath.Join(root, "logs"), 100)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{importDir: importDir, log: log}, importDir
}

// writeTestZip materializes a zip file from name->content entries.
func writeTestZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestExtractArchiveAbsolutePath: the wizard offers "…or absolute archive
// path on the host" — an absolute path OUTSIDE the import dir must import
// as-is. Regression guard: it used to be mangled into importDir + absPath
// and failed with "cannot open archive".
func TestExtractArchiveAbsolutePath(t *testing.T) {
	s, _ := newExtractService(t)
	// archive living somewhere else on the host
	hostDir := t.TempDir()
	hostZip := filepath.Join(hostDir, "host-archive.zip")
	writeTestZip(t, hostZip, map[string]string{
		"myworld/server.properties": "server-port=25565\n",
		"myworld/eula.txt":          "eula=true\n",
	})
	dest := t.TempDir()
	if err := s.extractArchive(hostZip, dest); err != nil {
		t.Fatalf("import from absolute host path failed: %v", err)
	}
	// single root dir "myworld" must be unwrapped
	for _, f := range []string{"server.properties", "eula.txt"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Errorf("expected unwrapped %s in server dir: %v", f, err)
		}
	}
}

// TestExtractArchiveRelativeInsideImportDir: relative names resolve against
// the import dir (the normal upload flow).
func TestExtractArchiveRelativeInsideImportDir(t *testing.T) {
	s, importDir := newExtractService(t)
	writeTestZip(t, filepath.Join(importDir, "up.zip"), map[string]string{"a.txt": "1"})
	dest := t.TempDir()
	if err := s.extractArchive("up.zip", dest); err != nil {
		t.Fatalf("relative import failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "a.txt")); err != nil {
		t.Errorf("a.txt not extracted: %v", err)
	}
}

// TestExtractArchiveSingleFileZip: a zip whose only entry is a single
// top-level FILE (e.g. just server.jar) must extract that file. Regression
// guard: the unwrap logic used to swallow it and leave the server empty.
func TestExtractArchiveSingleFileZip(t *testing.T) {
	s, _ := newExtractService(t)
	zipPath := filepath.Join(s.importDir, "single.zip")
	writeTestZip(t, zipPath, map[string]string{"server.jar": "jar-bytes"})
	dest := t.TempDir()
	if err := s.extractArchive(zipPath, dest); err != nil {
		t.Fatalf("single-file zip import failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "server.jar"))
	if err != nil {
		t.Fatalf("server.jar was swallowed by the unwrap logic: %v", err)
	}
	if string(data) != "jar-bytes" {
		t.Fatalf("server.jar content = %q", data)
	}
}

// TestExtractArchiveMultiRootKeepsStructure: zips with several top-level
// entries are extracted without unwrapping.
func TestExtractArchiveMultiRootKeepsStructure(t *testing.T) {
	s, _ := newExtractService(t)
	zipPath := filepath.Join(s.importDir, "multi.zip")
	writeTestZip(t, zipPath, map[string]string{
		"server.properties": "a=1\n",
		"plugins/essx.jar":  "jar",
		"world/level.dat":   "dat",
	})
	dest := t.TempDir()
	if err := s.extractArchive(zipPath, dest); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"server.properties", "plugins/essx.jar", "world/level.dat"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Errorf("%s not extracted (structure mangled): %v", f, err)
		}
	}
}

// TestExtractArchiveMissing: a clear error for missing archives instead of
// a mangled-path open failure.
func TestExtractArchiveMissing(t *testing.T) {
	s, _ := newExtractService(t)
	dest := t.TempDir()
	err := s.extractArchive("/nonexistent/nowhere.zip", dest)
	if err == nil || !strings.Contains(err.Error(), "archive not found") {
		t.Fatalf("err = %v, want 'archive not found'", err)
	}
}

// TestExtractArchiveTraversal: zip entries containing .. are rejected.
func TestExtractArchiveTraversal(t *testing.T) {
	s, _ := newExtractService(t)
	zipPath := filepath.Join(s.importDir, "evil.zip")
	writeTestZip(t, zipPath, map[string]string{"../../etc/passwd": "nope"})
	dest := t.TempDir()
	if err := s.extractArchive(zipPath, dest); err != files.ErrTraversal {
		t.Fatalf("err = %v, want ErrTraversal", err)
	}
}
