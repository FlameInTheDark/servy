package backupstore

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// getenvAny returns the first non-empty environment variable of the list.
func getenvAny(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

func TestKeyNamespace(t *testing.T) {
	cases := []struct {
		cluster, server, name string
		want                  string
	}{
		{"cluster-7f3a", "srv-42", "srv-42_2026-10-14.zip", "cluster-7f3a/srv-42/srv-42_2026-10-14.zip"},
		// path separators and traversal in user supplied names must not escape
		{"cluster/../etc", "srv-42", "../../evil.zip", "cluster/_/etc/srv-42/_/_/evil.zip"},
		{"c1", "s 1", "a b.zip", "c1/s_1/a_b.zip"},
		{"", "srv", "backup.zip", "_/srv/backup.zip"},
	}
	for _, tc := range cases {
		if got := Key(tc.cluster, tc.server, tc.name); got != tc.want {
			t.Errorf("Key(%q, %q, %q) = %q, want %q", tc.cluster, tc.server, tc.name, got, tc.want)
		}
		if strings.Contains(Key(tc.cluster, tc.server, tc.name), "..") {
			t.Errorf("key %q still contains a traversal", Key(tc.cluster, tc.server, tc.name))
		}
	}
	if got := PrefixFor("c1", "s1"); got != "c1/s1" {
		t.Errorf("PrefixFor = %q", got)
	}
	if got := ClusterPrefix("c1"); got != "c1" {
		t.Errorf("ClusterPrefix = %q", got)
	}
	// Two clusters never share a prefix, which is what keeps archives apart.
	a := Key(ClusterPrefix("cluster-a"), "srv-1", "x.zip")
	b := Key(ClusterPrefix("cluster-b"), "srv-1", "x.zip")
	if strings.HasPrefix(b, strings.TrimSuffix(a, "x.zip")) {
		t.Fatalf("clusters overlap: %q vs %q", a, b)
	}
}

func TestValidateAndSettings(t *testing.T) {
	for _, kind := range Kinds {
		if got := Settings(kind); len(got) == 0 {
			t.Errorf("kind %q has no settings", kind)
		}
		if err := Validate(kind, map[string]string{}); err == nil || !strings.Contains(err.Error(), "required") {
			t.Errorf("kind %q: expected a required-field error, got %v", kind, err)
		}
	}
	if err := Validate("carrier-pigeon", map[string]string{}); err == nil {
		t.Error("unknown kinds must be rejected")
	}
	if err := Validate(KindLocal, map[string]string{"path": "/tmp/x", "nope": "1"}); err == nil {
		t.Error("unknown settings must be rejected")
	}
	if err := Validate(KindLocal, map[string]string{"path": "/tmp/x"}); err != nil {
		t.Errorf("valid local settings rejected: %v", err)
	}
	if err := Validate(KindFTP, map[string]string{"host": "h", "username": "u", "tls": "maybe"}); err == nil {
		t.Error("invalid TLS mode must be rejected")
	}
	if err := Validate(KindS3, map[string]string{"bucket": "b", "access_key": "a", "secret_key": "s", "endpoint": "minio:9000"}); err == nil {
		t.Error("endpoint without a scheme must be rejected")
	}
	if err := Validate(KindSFTP, map[string]string{"host": "h", "username": "u", "port": "not-a-port"}); err == nil {
		t.Error("invalid port must be rejected")
	}
	for _, secret := range []string{"secret_key", "password", "passphrase", "private_key", "session_token"} {
		if !IsSecret(secret) {
			t.Errorf("%s must be treated as a secret", secret)
		}
	}
	if IsSecret("bucket") || IsSecret("host") {
		t.Error("non-secrets must not be masked")
	}
}

func TestOpenRejectsInvalidConfiguration(t *testing.T) {
	if _, err := Open(Destination{Kind: KindS3, Settings: map[string]string{"bucket": "b"}}); err == nil {
		t.Error("expected missing credentials to fail")
	}
	if _, err := Open(Destination{Kind: KindSFTP, Settings: map[string]string{"host": "h", "username": "u"}}); err == nil {
		t.Error("expected SFTP without password or key to fail")
	}
	if _, err := Open(Destination{Kind: "nope"}); err == nil {
		t.Error("expected unknown kind to fail")
	}
}

func TestRelKey(t *testing.T) {
	cases := []struct {
		full, prefix, want string
		ok                 bool
	}{
		{"servy/prod/cluster/srv/a.zip", "servy/prod", "cluster/srv/a.zip", true},
		{"servy/prod/cluster/srv/a.zip", "", "servy/prod/cluster/srv/a.zip", true},
		{"servy/nested/deep/cluster/a.zip", "servy/nested/deep", "cluster/a.zip", true},
		{"other/cluster/a.zip", "servy", "", false},
		{"servy", "servy", "", false},
	}
	for _, tc := range cases {
		got, ok := relKey(tc.full, tc.prefix)
		if got != tc.want || ok != tc.ok {
			t.Errorf("relKey(%q, %q) = %q,%v want %q,%v", tc.full, tc.prefix, got, ok, tc.want, tc.ok)
		}
	}
}

func TestLocalStoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	store, err := Open(Destination{Kind: KindLocal, Prefix: "backups", Settings: map[string]string{"path": root}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	key := Key("cluster-1", "srv-1", "a.zip")
	payload := []byte("local archive")
	if err := store.Put(ctx, key, strings.NewReader(string(payload)), int64(len(payload))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	target := filepath.Join(root, "backups", "cluster-1", "srv-1", "a.zip")
	if data, err := os.ReadFile(target); err != nil || string(data) != string(payload) {
		t.Fatalf("file not written correctly: %v", err)
	}
	rc, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	back, _ := io.ReadAll(rc)
	rc.Close()
	if string(back) != string(payload) {
		t.Fatal("content mismatch")
	}
	list, err := store.List(ctx, ClusterPrefix("cluster-1"))
	if err != nil || len(list) != 1 || list[0].Key != key {
		t.Fatalf("List = %+v, %v", list, err)
	}
	other, err := store.List(ctx, ClusterPrefix("cluster-2"))
	if err != nil || len(other) != 0 {
		t.Fatalf("isolation broken: %+v, %v", other, err)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("file still present")
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
	if err := store.Put(ctx, key, strings.NewReader("short"), 100); err == nil {
		t.Fatal("expected a short upload to fail")
	}
	// A traversal attempt must never write outside the configured directory.
	_ = store.Put(ctx, "../../escaped.zip", strings.NewReader("x"), 1)
	outside := filepath.Join(filepath.Dir(root), "escaped.zip")
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("traversal wrote outside the storage directory: %s", outside)
	}
	parentEscape := filepath.Join(root, "..", "escaped.zip")
	if _, err := os.Stat(parentEscape); err == nil {
		t.Fatalf("traversal wrote outside the storage directory: %s", parentEscape)
	}
}

func TestLocalDir(t *testing.T) {
	dest := Destination{Kind: KindLocal, Prefix: "a/b", Settings: map[string]string{"path": "/srv/backups"}}
	if got := dest.LocalDir(); got != "/srv/backups/a/b" {
		t.Errorf("LocalDir = %q", got)
	}
}
