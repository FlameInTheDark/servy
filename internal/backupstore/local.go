package backupstore

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// localStore writes archives into a directory of the machine it runs on. It is
// the reference implementation of the Store interface: an archive is written to
// a temporary file first and moved into place, so an interrupted upload never
// leaves a half archive behind.
type localStore struct {
	root   string
	prefix string
}

func newLocalStore(root, prefix string) *localStore {
	return &localStore{root: root, prefix: prefix}
}

func (s *localStore) Kind() string { return KindLocal }

// Location is the directory objects land in, namespace included.
func (s *localStore) Location() string {
	return filepath.Join(s.root, filepath.FromSlash(s.prefix))
}

// base is the directory keys are relative to.
func (s *localStore) base() string {
	return filepath.Join(s.root, filepath.FromSlash(s.prefix))
}

// resolve maps a relative key to an absolute path inside base.
func (s *localStore) resolve(key string) (string, error) {
	parts := splitKey(key)
	if len(parts) == 0 {
		return "", fmt.Errorf("empty object key")
	}
	base := s.base()
	full := filepath.Join(append([]string{base}, parts...)...)
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("object key %q escapes the storage directory", key)
	}
	return full, nil
}

func (s *localStore) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	target, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp := target + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	written, err := copyContext(ctx, out, body)
	if err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if size >= 0 && written != size {
		os.Remove(tmp)
		return fmt.Errorf("short write for %s: wrote %d of %d bytes", key, written, size)
	}
	return os.Rename(tmp, target)
}

func (s *localStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	target, err := s.resolve(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotExist
		}
		return nil, err
	}
	return f, nil
}

func (s *localStore) Delete(ctx context.Context, key string) error {
	target, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	// drop directories that became empty, but never the storage root
	dir := filepath.Dir(target)
	for dir != s.root && strings.HasPrefix(dir, s.root) && dir != filepath.Dir(dir) {
		if err := os.Remove(dir); err != nil {
			break
		}
		dir = filepath.Dir(dir)
	}
	return nil
}

func (s *localStore) List(ctx context.Context, prefix string) ([]Object, error) {
	base := s.base()
	out := []Object{}
	root := base
	if clean := cleanPrefix(prefix); clean != "" {
		root = filepath.Join(base, filepath.FromSlash(clean))
	}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(base, p)
		if rerr != nil {
			return nil
		}
		out = append(out, Object{
			Key:      filepath.ToSlash(rel),
			Size:     info.Size(),
			Modified: info.ModTime().UTC(),
		})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Ping creates the directory and proves it is writable.
func (s *localStore) Ping(ctx context.Context) error {
	base := s.base()
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	probe := path.Join(base, ".servy-probe")
	if err := os.WriteFile(probe, []byte("servy"), 0o644); err != nil {
		return err
	}
	return os.Remove(probe)
}

// copyContext copies while watching for a cancelled context.
func copyContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 256<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

// modTime parses a timestamp and returns the zero time on failure.
func modTime(raw string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, time.RFC1123, time.RFC1123Z, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, strings.TrimSpace(raw)); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
