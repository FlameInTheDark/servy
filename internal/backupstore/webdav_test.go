package backupstore

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

/* ---------------------------- fake WebDAV server --------------------------- */

// fakeWebDAV is a small WebDAV collection: MKCOL, PUT, GET, DELETE and
// PROPFIND, plus optional basic auth. It stores objects in memory with the same
// semantics a real server has (405 for an existing collection, 404 for missing
// objects, 207 listings).
type fakeWebDAV struct {
	mu       sync.Mutex
	files    map[string][]byte
	dirty    map[string]time.Time
	user     string
	password string
	requests []string
}

func newFakeWebDAV(user, password string) *fakeWebDAV {
	return &fakeWebDAV{files: map[string][]byte{}, dirty: map[string]time.Time{}, user: user, password: password}
}

func (f *fakeWebDAV) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if f.user != "" {
			user, pass, ok := r.BasicAuth()
			if !ok || user != f.user || pass != f.password {
				w.Header().Set("WWW-Authenticate", `Basic realm="servy"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		f.mu.Unlock()

		key := strings.Trim(strings.TrimPrefix(r.URL.Path, "/"), "/")
		key, _ = url.PathUnescape(key)

		switch r.Method {
		case "MKCOL":
			f.mu.Lock()
			defer f.mu.Unlock()
			if _, exists := f.files[key+"/"]; exists {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			// a collection only exists if its parent does
			if dir := path.Dir(key); dir != "." && dir != "/" {
				if _, ok := f.files[dir+"/"]; !ok {
					w.WriteHeader(http.StatusConflict)
					return
				}
			}
			f.files[key+"/"] = nil
			w.WriteHeader(http.StatusCreated)

		case "PUT":
			body, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			defer f.mu.Unlock()
			if dir := path.Dir(key); dir != "." && dir != "/" {
				if _, ok := f.files[dir+"/"]; !ok {
					w.WriteHeader(http.StatusConflict)
					return
				}
			}
			f.files[key] = body
			f.dirty[key] = time.Now().UTC()
			w.WriteHeader(http.StatusCreated)

		case "GET":
			f.mu.Lock()
			data, ok := f.files[key]
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write(data)

		case "DELETE":
			f.mu.Lock()
			defer f.mu.Unlock()
			if _, ok := f.files[key]; !ok {
				if _, ok := f.files[key+"/"]; !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
			}
			delete(f.files, key)
			delete(f.files, key+"/")
			delete(f.dirty, key)
			w.WriteHeader(http.StatusNoContent)

		case "PROPFIND":
			f.mu.Lock()
			defer f.mu.Unlock()
			if key != "" {
				if _, ok := f.files[key+"/"]; !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
			}
			type propfindResponse struct {
				XMLName  xml.Name `xml:"D:response"`
				Href     string   `xml:"D:href"`
				Propstat struct {
					Prop struct {
						Length       string `xml:"D:getcontentlength"`
						LastModified string `xml:"D:getlastmodified"`
						ResourceType struct {
							Collection *struct{} `xml:"D:collection"`
						} `xml:"D:resourcetype"`
					} `xml:"D:prop"`
					Status string `xml:"D:status"`
				} `xml:"D:propstat"`
			}
			responses := []propfindResponse{}
			prefix := key
			keys := make([]string, 0, len(f.files))
			for k := range f.files {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if prefix != "" && k != prefix+"/" && k != prefix && !strings.HasPrefix(k, prefix+"/") {
					continue
				}
				if prefix == "" && strings.Count(strings.TrimSuffix(k, "/"), "/") > 0 {
					continue
				}
				if strings.HasSuffix(k, "/") && k != prefix+"/" && strings.TrimSuffix(k, "/") != prefix {
					continue
				}
				resp := propfindResponse{Href: "/" + k}
				resp.Propstat.Status = "HTTP/1.1 200 OK"
				if data, ok := f.files[k]; ok {
					resp.Propstat.Prop.Length = fmt.Sprint(len(data))
					resp.Propstat.Prop.LastModified = f.dirty[k].Format(time.RFC1123)
				} else {
					resp.Propstat.Prop.ResourceType.Collection = &struct{}{}
				}
				responses = append(responses, resp)
			}
			w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
			w.WriteHeader(207)
			io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:">`)
			for _, resp := range responses {
				enc := xml.NewEncoder(w)
				if err := enc.Encode(resp); err != nil {
					break
				}
			}
			io.WriteString(w, `</D:multistatus>`)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func (f *fakeWebDAV) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for k := range f.files {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

/* --------------------------------- tests ---------------------------------- */

func TestWebDAVStoreRoundTrip(t *testing.T) {
	fake := newFakeWebDAV("", "")
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()

	store, err := Open(Destination{Kind: KindWebDAV, Prefix: "servy/panel", Settings: map[string]string{"url": srv.URL + "/dav"}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	payload := []byte(strings.Repeat("webdav-archive-", 1000))
	key := Key("cluster-7f3a", "srv-42", "srv-42.zip")
	if err := store.Put(ctx, key, strings.NewReader(string(payload)), int64(len(payload))); err != nil {
		t.Fatalf("Put: %v (keys: %v)", err, fake.keys())
	}
	if got := fake.keys(); !containsKey(got, "dav/servy/panel/cluster-7f3a/srv-42/srv-42.zip") {
		t.Fatalf("unexpected collection: %v", got)
	}
	if got := fake.files["dav/servy/panel/cluster-7f3a/srv-42/srv-42.zip"]; string(got) != string(payload) {
		t.Fatalf("stored content differs: %d bytes", len(got))
	}
	rc, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	back, _ := io.ReadAll(rc)
	rc.Close()
	if string(back) != string(payload) {
		t.Fatalf("content mismatch: %d bytes", len(back))
	}
	objects, err := store.List(ctx, ClusterPrefix("cluster-7f3a"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objects) != 1 || objects[0].Key != key || objects[0].Size != int64(len(payload)) {
		t.Fatalf("unexpected listing: %+v", objects)
	}
	other, err := store.List(ctx, ClusterPrefix("cluster-b"))
	if err != nil {
		t.Fatalf("List other: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("cluster isolation broken: %+v", other)
	}
	if _, err := store.Get(ctx, Key("cluster-7f3a", "srv-42", "missing.zip")); err != ErrNotExist {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if containsKey(fake.keys(), "dav/servy/panel/cluster-7f3a/srv-42/srv-42.zip") {
		t.Fatalf("object not deleted: %v", fake.keys())
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestWebDAVBasicAuth(t *testing.T) {
	fake := newFakeWebDAV("user", "pass")
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()

	store, err := Open(Destination{Kind: KindWebDAV, Settings: map[string]string{
		"url": srv.URL + "/dav", "username": "user", "password": "pass",
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	key := Key("c1", "s1", "a.zip")
	if err := store.Put(ctx, key, strings.NewReader("data"), 4); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	rc.Close()

	// wrong password must fail loudly
	bad, err := Open(Destination{Kind: KindWebDAV, Settings: map[string]string{
		"url": srv.URL + "/dav", "username": "user", "password": "nope",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := bad.Ping(ctx); err == nil {
		t.Fatal("expected authentication failure")
	} else if !strings.Contains(err.Error(), "401") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWebDAVRejectsBadURL(t *testing.T) {
	if _, err := Open(Destination{Kind: KindWebDAV, Settings: map[string]string{"url": "ftp://nope"}}); err == nil {
		t.Fatal("expected the URL scheme to be rejected")
	}
	if err := Validate(KindWebDAV, map[string]string{"url": "not a url"}); err == nil {
		t.Fatal("expected validation to reject the URL")
	}
	if err := Validate(KindWebDAV, map[string]string{"url": "https://dav.example/x", "bogus": "1"}); err == nil {
		t.Fatal("expected unknown settings to be rejected")
	}
}

func containsKey(keys []string, want string) bool {
	for _, k := range keys {
		if k == want {
			return true
		}
	}
	return false
}

// TestWebDAVExternalServer runs the round trip against a real WebDAV server
// (wsgidav, Nextcloud, Apache mod_dav, …) when SERVY_TEST_WEBDAV_URL is set.
func TestWebDAVExternalServer(t *testing.T) {
	base := os.Getenv("SERVY_TEST_WEBDAV_URL")
	if base == "" {
		t.Skip("SERVY_TEST_WEBDAV_URL not set")
	}
	store, err := Open(Destination{Kind: KindWebDAV, Prefix: "servy-test", Settings: map[string]string{
		"url":      base,
		"username": os.Getenv("SERVY_TEST_WEBDAV_USER"),
		"password": os.Getenv("SERVY_TEST_WEBDAV_PASSWORD"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	key := Key("cluster-external", "srv-x", "roundtrip.zip")
	payload := []byte("external webdav round trip")
	if err := store.Put(ctx, key, strings.NewReader(string(payload)), int64(len(payload))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	back, _ := io.ReadAll(rc)
	rc.Close()
	if string(back) != string(payload) {
		t.Fatalf("content mismatch: %q", back)
	}
	objects, err := store.List(ctx, ClusterPrefix("cluster-external"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objects) == 0 {
		t.Fatal("listing empty")
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
