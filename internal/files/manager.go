// Package files implements the server file manager: PathGuard-validated
// browse/read/write/rename/delete/copy/move, zip/unzip, downloads and
// chunked uploads (parity with the legacy file helpers).
package files

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrTraversal is returned (and audited) on any path escape attempt.
var ErrTraversal = errors.New("TRAVERSAL DETECTED")

// Entry is one directory listing item.
type Entry struct {
	Name     string    `json:"name"`
	Dir      bool      `json:"dir"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// Guard validates paths against a root (single choke point).
type Guard struct{ root string }

// NewGuard builds a guard rooted at root (absolute).
func NewGuard(root string) (*Guard, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Guard{root: abs}, nil
}

// Root returns the guarded root.
func (g *Guard) Root() string { return g.root }

// Validate resolves rel inside root or returns ErrTraversal.
// rel may be absolute-inside-root or relative.
func (g *Guard) Validate(rel string) (string, error) {
	if rel == "" {
		return g.root, nil
	}
	var p string
	if filepath.IsAbs(rel) {
		p = filepath.Clean(rel)
	} else {
		p = filepath.Clean(filepath.Join(g.root, rel))
	}
	if p != g.root && !strings.HasPrefix(p, g.root+string(os.PathSeparator)) {
		return "", ErrTraversal
	}
	return p, nil
}

// Manager provides file operations under a guard.
type Manager struct{}

// New creates the manager.
func New() *Manager { return &Manager{} }

// List returns directory entries sorted (dirs first, then names).
func (m *Manager) List(g *Guard, rel string) ([]Entry, error) {
	dir, err := g.Validate(rel)
	if err != nil {
		return nil, err
	}
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := []Entry{}
	for _, it := range items {
		info, err := it.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{Name: it.Name(), Dir: it.IsDir(), Size: info.Size(), Modified: info.ModTime().UTC()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// Read returns file contents (size-limited).
func (m *Manager) Read(g *Guard, rel string, maxBytes int64) ([]byte, error) {
	p, err := g.Validate(rel)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, errors.New("IS_DIRECTORY")
	}
	if maxBytes > 0 && st.Size() > maxBytes {
		return nil, errors.New("FILE_TOO_LARGE")
	}
	return os.ReadFile(p)
}

// Write creates/truncates a file (atomic tmp+rename).
func (m *Manager) Write(g *Guard, rel string, data []byte) error {
	p, err := g.Validate(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".servy-tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// CreateDir makes a directory.
func (m *Manager) CreateDir(g *Guard, rel string) error {
	p, err := g.Validate(rel)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err == nil {
		return errors.New("ALREADY_EXISTS")
	}
	return os.MkdirAll(p, 0o755)
}

// CreateFile makes an empty file (error if exists).
func (m *Manager) CreateFile(g *Guard, rel string) error {
	p, err := g.Validate(rel)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err == nil {
		return errors.New("ALREADY_EXISTS")
	}
	return os.WriteFile(p, []byte{}, 0o644)
}

// Delete removes a file or directory tree.
func (m *Manager) Delete(g *Guard, rel string) error {
	p, err := g.Validate(rel)
	if err != nil {
		return err
	}
	if p == g.root {
		return errors.New("CANNOT_DELETE_ROOT")
	}
	return os.RemoveAll(p)
}

// Rename/Move moves src to dst (both inside the guard).
func (m *Manager) Move(g *Guard, src, dst string) error {
	sp, err := g.Validate(src)
	if err != nil {
		return err
	}
	dp, err := g.Validate(dst)
	if err != nil {
		return err
	}
	if sp == g.root {
		return errors.New("CANNOT_MOVE_ROOT")
	}
	if err := os.MkdirAll(filepath.Dir(dp), 0o755); err != nil {
		return err
	}
	return os.Rename(sp, dp)
}

// Copy duplicates src to dst.
func (m *Manager) Copy(g *Guard, src, dst string) error {
	sp, err := g.Validate(src)
	if err != nil {
		return err
	}
	dp, err := g.Validate(dst)
	if err != nil {
		return err
	}
	st, err := os.Stat(sp)
	if err != nil {
		return err
	}
	if st.IsDir() {
		return copyDir(sp, dp)
	}
	return copyFile(sp, dp)
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(p, target)
	})
}

// Stat returns entry info.
func (m *Manager) Stat(g *Guard, rel string) (Entry, error) {
	p, err := g.Validate(rel)
	if err != nil {
		return Entry{}, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Name: filepath.Base(p), Dir: st.IsDir(), Size: st.Size(), Modified: st.ModTime().UTC()}, nil
}

// Zip creates an archive from listed paths (relative to guard root) at outRel.
func (m *Manager) Zip(g *Guard, paths []string, outRel string) error {
	out, err := g.Validate(outRel)
	if err != nil {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	for _, rel := range paths {
		p, err := g.Validate(rel)
		if err != nil {
			continue // skip bad paths, keep zipping
		}
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.IsDir() {
			base := filepath.Base(p)
			err := filepath.Walk(p, func(fp string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil
				}
				rel2, _ := filepath.Rel(p, fp)
				return addFileToZip(zw, fp, filepath.Join(base, rel2))
			})
			if err != nil {
				return err
			}
		} else {
			if err := addFileToZip(zw, p, filepath.Base(p)); err != nil {
				return err
			}
		}
	}
	return nil
}

func addFileToZip(zw *zip.Writer, path, name string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	w, err := zw.Create(filepath.ToSlash(name))
	if err != nil {
		return err
	}
	_, err = io.Copy(w, in)
	return err
}

// Unzip extracts an archive (inside the guard) into destDir (inside the guard).
func (m *Manager) Unzip(g *Guard, zipRel, destDir string) error {
	zp, err := g.Validate(zipRel)
	if err != nil {
		return err
	}
	dest, err := g.Validate(destDir)
	if err != nil {
		return err
	}
	zr, err := zip.OpenReader(zp)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		name := filepath.Clean(f.Name)
		if strings.Contains(name, "..") {
			continue
		}
		target := filepath.Join(dest, name)
		// double-check destination stays inside guard
		if _, err := g.Validate(target); err != nil {
			continue
		}
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

// UploadStore assembles chunked uploads: chunks land under spool/<id>/ and are
// concatenated into the final guarded path on completion.
type UploadStore struct {
	spoolDir string
}

// NewUploadStore creates the spool directory.
func NewUploadStore(spoolDir string) (*UploadStore, error) {
	if err := os.MkdirAll(spoolDir, 0o755); err != nil {
		return nil, err
	}
	return &UploadStore{spoolDir: spoolDir}, nil
}

// SaveChunk writes chunk n of upload id.
func (u *UploadStore) SaveChunk(id string, n int, data []byte) error {
	dir := filepath.Join(u.spoolDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(fmt.Sprintf("%s/part_%09d", dir, n), data, 0o644)
}

// Assemble concatenates all chunks (ordered) into finalPath (already validated).
func (u *UploadStore) Assemble(id string, total int, finalPath string) error {
	dir := filepath.Join(u.spoolDir, id)
	defer os.RemoveAll(dir)
	out, err := os.Create(finalPath)
	if err != nil {
		return err
	}
	defer out.Close()
	for i := 0; i < total; i++ {
		p := fmt.Sprintf("%s/part_%09d", dir, i)
		in, err := os.Open(p)
		if err != nil {
			return fmt.Errorf("missing chunk %d: %w", i, err)
		}
		if _, err := io.Copy(out, in); err != nil {
			in.Close()
			return err
		}
		in.Close()
	}
	return nil
}

// DownloadURL downloads a remote URL into a guarded path (import helpers).
func DownloadURL(url, destPath string) (int64, error) {
	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	out, err := os.Create(destPath)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	return io.Copy(out, resp.Body)
}

// NewGuardOrPanic builds a guard panicking on error (call sites with known-good roots).
func NewGuardOrPanic(root string) *Guard {
	g, err := NewGuard(root)
	if err != nil {
		panic(err)
	}
	return g
}
