package httpapi

import (
	"archive/zip"
	"bytes"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/FlameInTheDark/servy/internal/files"
)

func newUUID() string { return uuid.NewString() }

func newGuard(root string) *files.Guard { return files.NewGuardOrPanic(root) }

// zipEntry is one name->content pair.
type zipEntry struct{ name, content string }

// writeZip creates a simple zip from entries.
func writeZip(path string, entries []zipEntry) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	for _, e := range entries {
		w, err := zw.Create(strings.TrimPrefix(e.name, "/"))
		if err != nil {
			continue
		}
		w.Write([]byte(e.content))
	}
	return nil
}

var _ = bytes.MinRead
