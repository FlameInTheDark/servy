package files

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestGuardTraversal(t *testing.T) {
	tmp := t.TempDir()
	g, _ := NewGuard(tmp)
	// ok paths
	if _, err := g.Validate("sub/file.txt"); err != nil {
		t.Fatalf("valid path rejected: %v", err)
	}
	if _, err := g.Validate(""); err != nil {
		t.Fatalf("root rejected")
	}
	if _, err := g.Validate(tmp + "/x"); err != nil {
		t.Fatalf("absolute inside rejected")
	}
	// traversal attempts
	for _, bad := range []string{"../etc/passwd", "..", "a/../../b", tmp + "/../secret"} {
		if _, err := g.Validate(bad); err != ErrTraversal {
			t.Fatalf("traversal not caught: %q -> %v", bad, err)
		}
	}
}

func TestWriteReadListDelete(t *testing.T) {
	tmp := t.TempDir()
	g, _ := NewGuard(tmp)
	m := New()
	if err := m.Write(g, "dir/a.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	data, err := m.Read(g, "dir/a.txt", 0)
	if err != nil || string(data) != "hello" {
		t.Fatalf("read: %q %v", data, err)
	}
	entries, err := m.List(g, "")
	if err != nil || len(entries) != 1 || entries[0].Name != "dir" {
		t.Fatalf("list: %+v %v", entries, err)
	}
	if err := m.Delete(g, "dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "dir")); !os.IsNotExist(err) {
		t.Fatal("delete failed")
	}
}

func TestZipRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	g, _ := NewGuard(tmp)
	m := New()
	m.Write(g, "a.txt", []byte("A"))
	m.Write(g, "sub/b.txt", []byte("B"))
	if err := m.Zip(g, []string{"a.txt", "sub"}, "out.zip"); err != nil {
		t.Fatal(err)
	}
	// unzip to fresh dir
	if err := m.Unzip(g, "out.zip", "restored"); err != nil {
		t.Fatal(err)
	}
	data, err := m.Read(g, "restored/a.txt", 0)
	if err != nil || string(data) != "A" {
		t.Fatalf("unzip a: %v", err)
	}
	data, _ = m.Read(g, "restored/sub/b.txt", 0)
	if string(data) != "B" {
		t.Fatal("unzip b failed")
	}
}

func TestZipTraversalRejected(t *testing.T) {
	tmp := t.TempDir()
	g, _ := NewGuard(tmp)
	m := New()
	// craft a malicious archive with ../evil.txt entries
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("../evil.txt")
	w.Write([]byte("pwn"))
	w2, _ := zw.Create("ok.txt")
	w2.Write([]byte("fine"))
	zw.Close()
	if err := m.Write(g, "evil.zip", buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := m.Unzip(g, "evil.zip", "."); err != nil {
		t.Fatal("unzip should succeed but skip evil entries")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(tmp), "evil.txt")); err == nil {
		t.Fatal("traversal escaped!")
	}
	if _, err := m.Stat(g, "ok.txt"); err != nil {
		t.Fatal("benign entry should extract")
	}
}
