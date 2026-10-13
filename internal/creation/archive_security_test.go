package creation

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlameInTheDark/servy/internal/files"
)

func writeArchiveEntry(t *testing.T, archivePath string, header *zip.FileHeader, data string) {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExtractArchiveRejectsAbsoluteAndWindowsPaths(t *testing.T) {
	cases := []string{"/etc/passwd", `C:\\Windows\\system.ini`, `..\\outside.txt`}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newExtractService(t)
			archivePath := filepath.Join(s.importDir, "unsafe.zip")
			header := &zip.FileHeader{Name: name}
			writeArchiveEntry(t, archivePath, header, "nope")
			if err := s.extractArchive(archivePath, t.TempDir()); err != files.ErrTraversal {
				t.Fatalf("extractArchive(%q) error = %v, want ErrTraversal", name, err)
			}
		})
	}
}

func TestExtractArchiveRejectsSymlinkEntries(t *testing.T) {
	s, _ := newExtractService(t)
	archivePath := filepath.Join(s.importDir, "symlink.zip")
	header := &zip.FileHeader{Name: "link"}
	header.SetMode(os.ModeSymlink | 0o777)
	writeArchiveEntry(t, archivePath, header, "../../outside")
	if err := s.extractArchive(archivePath, t.TempDir()); err == nil {
		t.Fatal("symlink entry should be rejected")
	}
}
