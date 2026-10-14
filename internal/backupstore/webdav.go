package backupstore

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// webdavStore stores archives in a WebDAV collection: Nextcloud/ownCloud,
// Apache mod_dav, Caddy webdav, Synology, mailbox.org, …
type webdavStore struct {
	base     *url.URL
	username string
	password string
	client   *http.Client
}

func newWebDAVStore(settings map[string]string, prefix string) (*webdavStore, error) {
	raw := strings.TrimSpace(settings["url"])
	base, err := url.Parse(raw)
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("invalid WebDAV URL %q", raw)
	}
	if prefix != "" {
		base.Path = strings.TrimSuffix(base.Path, "/") + "/" + prefix
	}
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.EqualFold(settings["insecure"], "true") {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &webdavStore{
		base:     base,
		username: strings.TrimSpace(settings["username"]),
		password: settings["password"],
		client:   &http.Client{Transport: transport, Timeout: 0},
	}, nil
}

func (s *webdavStore) Kind() string { return KindWebDAV }

func (s *webdavStore) Location() string {
	return strings.TrimSuffix(s.base.String(), "/") + "/"
}

// url builds the URL of a relative key. The key segments are escaped, the
// collection URL itself is used as configured (Nextcloud shares contain %2F).
func (s *webdavStore) url(key string) string {
	clean := strings.Join(splitKey(key), "/")
	if clean == "" {
		return s.base.String()
	}
	segments := []string{}
	for _, p := range strings.Split(clean, "/") {
		segments = append(segments, url.PathEscape(p))
	}
	return strings.TrimSuffix(s.base.String(), "/") + "/" + strings.Join(segments, "/")
}

func (s *webdavStore) request(ctx context.Context, method, target string, body io.Reader, length int64, header http.Header) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	if body != nil && length >= 0 {
		req.ContentLength = length
	}
	if s.username != "" || s.password != "" {
		req.SetBasicAuth(s.username, s.password)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return req, nil
}

// mkcol creates a single collection, tolerating the answers servers give for
// "it is already there" (405), "created" and redirects to the slash-ed path.
func (s *webdavStore) mkcol(ctx context.Context, target string) error {
	req, err := s.request(ctx, "MKCOL", target, nil, -1, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK, http.StatusNoContent,
		http.StatusMethodNotAllowed, http.StatusMovedPermanently, http.StatusFound,
		http.StatusConflict: // parent missing; the caller creates parents first
		return nil
	}
	return fmt.Errorf("webdav MKCOL %s failed (%d): %s", target, resp.StatusCode, truncate(string(raw), 200))
}

// ensureBase creates the configured collection itself, level by level, so a
// destination that does not exist yet starts working without manual setup.
func (s *webdavStore) ensureBase(ctx context.Context) error {
	segments := strings.Split(strings.Trim(s.base.Path, "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		return nil
	}
	for i := 1; i <= len(segments); i++ {
		target := *s.base
		target.Path = "/" + strings.Join(segments[:i], "/")
		target.RawQuery = ""
		if err := s.mkcol(ctx, target.String()); err != nil {
			return err
		}
	}
	return nil
}

// ensureCollection creates every missing collection of a key's directory.
func (s *webdavStore) ensureCollection(ctx context.Context, key string) error {
	if err := s.ensureBase(ctx); err != nil {
		return err
	}
	dir := dirOf(strings.Join(splitKey(key), "/"))
	if dir == "" {
		return nil
	}
	current := ""
	for _, part := range strings.Split(dir, "/") {
		if part == "" {
			continue
		}
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		if err := s.mkcol(ctx, s.url(current)); err != nil {
			return err
		}
	}
	return nil
}

func (s *webdavStore) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	if err := s.ensureCollection(ctx, key); err != nil {
		return err
	}
	reader := body
	if size < 0 {
		if rs, ok := body.(io.ReadSeeker); ok {
			if n, err := rs.Seek(0, io.SeekEnd); err == nil {
				size = n
				rs.Seek(0, io.SeekStart)
			}
		} else {
			tmp, err := os.CreateTemp("", "servy-upload-*.part")
			if err != nil {
				return err
			}
			defer func() { tmp.Close(); os.Remove(tmp.Name()) }()
			n, err := io.Copy(tmp, body)
			if err != nil {
				return err
			}
			if _, err := tmp.Seek(0, io.SeekStart); err != nil {
				return err
			}
			size = n
			reader = tmp
		}
	}
	req, err := s.request(ctx, http.MethodPut, s.url(key), reader, size, http.Header{"Content-Type": {"application/octet-stream"}})
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webdav PUT %s failed (%d): %s", s.url(key), resp.StatusCode, truncate(string(raw), 200))
	}
	return nil
}

func (s *webdavStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	req, err := s.request(ctx, http.MethodGet, s.url(key), nil, -1, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, ErrNotExist
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("webdav GET %s failed (%d): %s", s.url(key), resp.StatusCode, truncate(string(raw), 200))
	}
	return resp.Body, nil
}

func (s *webdavStore) Delete(ctx context.Context, key string) error {
	req, err := s.request(ctx, http.MethodDelete, s.url(key), nil, -1, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusNotFound || (resp.StatusCode >= 200 && resp.StatusCode <= 299) {
		return nil
	}
	return fmt.Errorf("webdav DELETE %s failed (%d)", s.url(key), resp.StatusCode)
}

// propfindEntry is one <d:response> of a PROPFIND listing.
type propfindEntry struct {
	Href     string `xml:"href"`
	Propstat []struct {
		Prop struct {
			ContentLength string `xml:"getcontentlength"`
			LastModified  string `xml:"getlastmodified"`
			ResourceType  struct {
				Collection *struct{} `xml:"collection"`
			} `xml:"resourcetype"`
		} `xml:"prop"`
		Status string `xml:"status"`
	} `xml:"propstat"`
}

func (s *webdavStore) propfind(ctx context.Context, target string, depth int) ([]propfindEntry, error) {
	body := `<?xml version="1.0" encoding="utf-8"?><D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/><D:getcontentlength/><D:getlastmodified/></D:prop></D:propfind>`
	header := http.Header{
		"Content-Type": {"application/xml; charset=utf-8"},
		"Depth":        {fmt.Sprint(depth)},
	}
	req, err := s.request(ctx, "PROPFIND", target, strings.NewReader(body), int64(len(body)), header)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotExist
	}
	if resp.StatusCode != 207 && (resp.StatusCode < 200 || resp.StatusCode > 299) {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("webdav PROPFIND %s failed (%d): %s", target, resp.StatusCode, truncate(string(raw), 200))
	}
	var out struct {
		Responses []propfindEntry `xml:"response"`
	}
	dec := xml.NewDecoder(resp.Body)
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("webdav PROPFIND response: %w", err)
	}
	return out.Responses, nil
}

// relativeKey turns an href from a listing into a key relative to the base.
func (s *webdavStore) relativeKey(href string) (string, bool) {
	u, err := url.Parse(href)
	if err != nil {
		return "", false
	}
	target := u.Path
	if !strings.HasPrefix(target, "/") {
		// some servers return a path relative to the collection
		target = strings.TrimSuffix(s.base.Path, "/") + "/" + strings.TrimPrefix(target, "/")
	}
	basePath := strings.TrimSuffix(s.base.Path, "/") + "/"
	decoded, err := url.PathUnescape(target)
	if err != nil {
		decoded = target
	}
	baseDecoded, err := url.PathUnescape(basePath)
	if err != nil {
		baseDecoded = basePath
	}
	if !strings.HasPrefix(decoded, baseDecoded) {
		return "", false
	}
	rel := strings.TrimPrefix(decoded, baseDecoded)
	return strings.Trim(rel, "/"), true
}

func (s *webdavStore) List(ctx context.Context, prefix string) ([]Object, error) {
	clean := strings.Join(splitKey(prefix), "/")
	if clean != "" {
		// Make sure the requested subtree exists so an empty destination is not
		// reported as an error.
		if err := s.ensureCollection(ctx, clean+"/probe"); err != nil {
			return nil, err
		}
	}
	out := []Object{}
	queue := []string{clean}
	seen := 0
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		seen++
		if seen > 5000 {
			break
		}
		// dir is already a key relative to the collection URL.
		entries, err := s.propfind(ctx, s.url(dir), 1)
		if err != nil {
			if err == ErrNotExist {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			rel, ok := s.relativeKey(e.Href)
			if !ok || rel == "" {
				continue
			}
			// Prefix filters out the base collection answer and sibling trees.
			if clean != "" && rel != clean && !strings.HasPrefix(rel, clean+"/") {
				continue
			}
			isDir := strings.HasSuffix(e.Href, "/")
			var size int64
			var modified time.Time
			for _, ps := range e.Propstat {
				if ps.Prop.ResourceType.Collection != nil {
					isDir = true
				}
				if v, err := parseInt(ps.Prop.ContentLength); err == nil {
					size = v
				}
				if t, err := time.Parse(time.RFC1123, ps.Prop.LastModified); err == nil {
					modified = t.UTC()
				}
			}
			if isDir {
				if rel != clean {
					queue = append(queue, rel)
				}
				continue
			}
			if clean != "" && !strings.HasPrefix(rel, clean+"/") {
				continue
			}
			out = append(out, Object{Key: rel, Size: size, Modified: modified})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *webdavStore) Ping(ctx context.Context) error {
	if err := s.ensureBase(ctx); err != nil {
		return err
	}
	if _, err := s.propfind(ctx, s.base.String(), 0); err != nil {
		return fmt.Errorf("webdav collection %s is not reachable: %w", s.base.String(), err)
	}
	return nil
}

func parseInt(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}
