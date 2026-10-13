package steamcmd

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// jsonDecode is a tiny indirection for imports.
func jsonDecode(data []byte, v interface{}) error { return json.Unmarshal(data, v) }

// zipArchive wraps zip.ReadCloser with graceful nil handling.
type zipArchive struct{ zr *zip.ReadCloser }

// zipOpen opens a zip archive, returning an empty wrapper on error.
func zipOpen(path string) *zipArchive {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return &zipArchive{}
	}
	return &zipArchive{zr: zr}
}

// Close closes the underlying archive.
func (z *zipArchive) Close() error {
	if z.zr != nil {
		return z.zr.Close()
	}
	return nil
}

// Files returns the archive entries.
func (z *zipArchive) Files() []*zip.File {
	if z.zr == nil {
		return nil
	}
	return z.zr.File
}

// Extract unpacks the archive into dest.
func (z *zipArchive) Extract(dest string) error {
	for _, f := range z.Files() {
		name := filepath.Clean(f.Name)
		if strings.Contains(name, "..") {
			continue
		}
		target := filepath.Join(dest, name)
		rc, err := f.Open()
		if err != nil {
			continue
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0o755)
			rc.Close()
			continue
		}
		os.MkdirAll(filepath.Dir(target), 0o755)
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
