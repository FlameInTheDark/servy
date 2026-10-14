package backupstore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// The SFTP tests run against OpenSSH's own sftp-server binary, driven through an
// in-process SSH server. The protocol implementation under test therefore has an
// independent counterparty instead of a mock we wrote ourselves. When the
// binary is missing (or SERVY_TEST_SFTP_ADDR points at a real server) the tests
// fall back to that server or skip.

func sftpServerBinary(t *testing.T) string {
	t.Helper()
	candidates := []string{
		"/usr/lib/openssh/sftp-server",
		"/usr/libexec/openssh/sftp-server",
		"/usr/libexec/sftp-server",
		"/usr/lib/ssh/sftp-server",
	}
	if v := os.Getenv("SERVY_TEST_SFTP_SERVER_BIN"); v != "" {
		candidates = append([]string{v}, candidates...)
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	if p, err := exec.LookPath("sftp-server"); err == nil {
		return p
	}
	return ""
}

// sshTestServer is a minimal SSH server that exposes sftp-server sessions.
type sshTestServer struct {
	Addr string
	Key  ssh.PublicKey
	Host string

	ln     net.Listener
	wg     sync.WaitGroup
	closed chan struct{}
}

func startSSHTestServer(t *testing.T, root, user, password string) *sshTestServer {
	t.Helper()
	bin := sftpServerBinary(t)
	if bin == "" {
		t.Skip("no sftp-server binary available")
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if conn.User() == user && string(pass) == password {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &sshTestServer{Addr: ln.Addr().String(), Key: signer.PublicKey(), Host: "127.0.0.1", ln: ln, closed: make(chan struct{})}

	srv.wg.Add(1)
	go func() {
		defer srv.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.handle(conn, cfg, root, bin)
		}
	}()
	t.Cleanup(func() {
		close(srv.closed)
		ln.Close()
		srv.wg.Wait()
	})
	return srv
}

func (s *sshTestServer) handle(conn net.Conn, cfg *ssh.ServerConfig, root, bin string) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		conn.Close()
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(ssh.UnknownChannelType, "only sessions are supported")
			continue
		}
		ch, chReqs, err := newChan.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(ch, chReqs, root, bin)
	}
}

func (s *sshTestServer) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request, root, bin string) {
	defer ch.Close()
	var cmd *exec.Cmd
	var stdin io.WriteCloser
	var stdout io.ReadCloser

	start := func() error {
		cmd = exec.Command(bin)
		cmd.Dir = root
		var err error
		stdin, err = cmd.StdinPipe()
		if err != nil {
			return err
		}
		stdout, err = cmd.StdoutPipe()
		if err != nil {
			return err
		}
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { io.Copy(stdin, ch) }()
		go func() { io.Copy(ch, stdout); ch.CloseWrite(); cmd.Wait() }()
		return nil
	}

	for req := range reqs {
		switch req.Type {
		case "subsystem":
			if strings.TrimSpace(string(req.Payload[4:])) != "sftp" {
				req.Reply(false, nil)
				continue
			}
			if err := start(); err != nil {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
		case "env", "pty-req", "shell", "exec":
			req.Reply(false, nil)
		default:
			req.Reply(false, nil)
		}
	}
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
	}
}

// sftpTestStore points a store at the test server.
func sftpTestStore(t *testing.T, srv *sshTestServer, prefix string, settings map[string]string) *sftpStore {
	t.Helper()
	merged := map[string]string{
		"host":     srv.Host,
		"port":     strings.TrimPrefix(srv.Addr, srv.Host+":"),
		"username": "tester",
		"password": "secret",
	}
	for k, v := range settings {
		merged[k] = v
	}
	if _, ok := merged["host_key"]; !ok {
		merged["host_key"] = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(srv.Key)))
	}
	dest := Destination{Kind: KindSFTP, Prefix: prefix, Settings: merged}
	store, err := Open(dest)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store.(*sftpStore)
}

func TestSFTPStoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	srv := startSSHTestServer(t, root, "tester", "secret")
	store := sftpTestStore(t, srv, "backups/nested", nil)
	ctx := context.Background()

	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	key := Key("cluster-7f3a", "srv-42", "srv-42_2026-10-14_12-00-00.zip")
	payload := []byte(strings.Repeat("archive-bytes-", 5000))
	if err := store.Put(ctx, key, strings.NewReader(string(payload)), int64(len(payload))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// namespace on disk: prefix/cluster/server/config/archive
	onDisk := filepath.Join(root, "backups", "nested", "cluster-7f3a", "srv-42", "srv-42_2026-10-14_12-00-00.zip")
	got, err := os.ReadFile(onDisk)
	if err != nil {
		t.Fatalf("reading uploaded file: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("uploaded file differs: %d vs %d bytes", len(got), len(payload))
	}

	rc, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	back, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if string(back) != string(payload) {
		t.Fatalf("downloaded content differs: %d vs %d bytes", len(back), len(payload))
	}

	objects, err := store.List(ctx, ClusterPrefix("cluster-7f3a"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objects) != 1 || objects[0].Key != key || objects[0].Size != int64(len(payload)) {
		t.Fatalf("unexpected listing: %+v", objects)
	}

	// a second cluster in the same destination must not see the first one
	other, err := store.List(ctx, ClusterPrefix("cluster-other"))
	if err != nil {
		t.Fatalf("List other: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("cluster isolation broken: %+v", other)
	}

	if _, err := store.Get(ctx, Key("cluster-other", "srv-1", "missing.zip")); err != ErrNotExist {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}

	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(onDisk); !os.IsNotExist(err) {
		t.Fatalf("file still there after Delete: %v", err)
	}
	// deleting again is fine
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
	// and the empty server directory is pruned
	if _, err := os.Stat(filepath.Dir(onDisk)); !os.IsNotExist(err) {
		t.Fatalf("empty server directory not pruned: %v", err)
	}
}

func TestSFTPStoreTruncatedAndLargeUpload(t *testing.T) {
	root := t.TempDir()
	srv := startSSHTestServer(t, root, "tester", "secret")
	store := sftpTestStore(t, srv, "", nil)
	ctx := context.Background()

	// A payload larger than one read/write window exercises chunking.
	size := 5*maxRead + 1234
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	key := Key("c1", "s1", "big.zip")
	if err := store.Put(ctx, key, strings.NewReader(string(payload)), int64(len(payload))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	back, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(back) != size {
		t.Fatalf("size mismatch: %d vs %d", len(back), size)
	}
	for i := range back {
		if back[i] != payload[i] {
			t.Fatalf("byte %d differs", i)
		}
	}
	// A truncated upload has to fail instead of storing a partial archive.
	if err := store.Put(ctx, key, strings.NewReader("too short"), int64(len("expected longer"))); err == nil {
		t.Fatal("expected an error for a short upload")
	}
}

func TestSFTPHostKeyPolicies(t *testing.T) {
	root := t.TempDir()
	srv := startSSHTestServer(t, root, "tester", "secret")
	ctx := context.Background()
	port := strings.TrimPrefix(srv.Addr, srv.Host+":")

	// 1. no pinned key and no known-hosts file: refuse instead of trusting
	SetKnownHostsFile("")
	strict, err := Open(Destination{Kind: KindSFTP, Settings: map[string]string{
		"host": srv.Host, "port": port, "username": "tester", "password": "secret",
	}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := strict.Ping(ctx); err == nil {
		t.Fatal("expected the connection to be refused without a host key")
	}

	// 2. first use is trusted and remembered in the known-hosts file
	known := filepath.Join(t.TempDir(), "known_hosts")
	SetKnownHostsFile(known)
	t.Cleanup(func() { SetKnownHostsFile("") })
	tofu, err := Open(Destination{Kind: KindSFTP, Settings: map[string]string{
		"host": srv.Host, "port": port, "username": "tester", "password": "secret",
	}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := tofu.Ping(ctx); err != nil {
		t.Fatalf("trust on first use failed: %v", err)
	}
	raw, err := os.ReadFile(known)
	if err != nil {
		t.Fatalf("known_hosts not written: %v", err)
	}
	if !strings.Contains(string(raw), "ssh-ed25519") {
		t.Fatalf("known_hosts has no key: %s", raw)
	}
	// second connection verifies against the stored key
	if err := tofu.Ping(ctx); err != nil {
		t.Fatalf("verification against known_hosts failed: %v", err)
	}

	// 3. a different pinned key must be rejected
	otherKey := generateOtherHostKey(t)
	if _, err := Open(Destination{Kind: KindSFTP, Settings: map[string]string{
		"host": srv.Host, "port": port, "username": "tester", "password": "secret",
		"host_key": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(otherKey))),
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	pinned, _ := Open(Destination{Kind: KindSFTP, Settings: map[string]string{
		"host": srv.Host, "port": port, "username": "tester", "password": "secret",
		"host_key": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(otherKey))),
	}})
	err = pinned.Ping(ctx)
	if err == nil {
		t.Fatal("expected a host key mismatch to be refused")
	}
	if !strings.Contains(err.Error(), "handshake") && !strings.Contains(err.Error(), "verification") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func generateOtherHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer.PublicKey()
}

func TestSFTPWrongCredentials(t *testing.T) {
	root := t.TempDir()
	srv := startSSHTestServer(t, root, "tester", "secret")
	store := sftpTestStore(t, srv, "", map[string]string{"password": "nope"})
	err := store.Ping(context.Background())
	if err == nil {
		t.Fatal("expected authentication to fail")
	}
	if !strings.Contains(err.Error(), "handshake") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestSFTPExternalServer runs the same round trip against a real server when
// SERVY_TEST_SFTP_ADDR is configured (for example in CI with a throwaway sshd).
func TestSFTPExternalServer(t *testing.T) {
	addr := os.Getenv("SERVY_TEST_SFTP_ADDR")
	if addr == "" {
		t.Skip("SERVY_TEST_SFTP_ADDR not set")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("bad SERVY_TEST_SFTP_ADDR: %v", err)
	}
	settings := map[string]string{
		"host":     host,
		"port":     port,
		"username": os.Getenv("SERVY_TEST_SFTP_USER"),
		"password": os.Getenv("SERVY_TEST_SFTP_PASSWORD"),
	}
	if key := os.Getenv("SERVY_TEST_SFTP_KEY"); key != "" {
		settings["private_key"] = key
	}
	if known := os.Getenv("SERVY_TEST_SFTP_KNOWN_HOSTS"); known != "" {
		SetKnownHostsFile(known)
	}
	dest := Destination{Kind: KindSFTP, Prefix: "servy-test", Settings: settings}
	store, err := Open(dest)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	key := Key("cluster-external", "srv-x", "roundtrip.zip")
	payload := []byte("external sftp round trip")
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
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
