package backupstore

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

/* --------------------------- fake FTP/FTPS server -------------------------- */

// fakeFTPServer is a small FTP server used by the tests when no real server is
// configured. It implements the same passive-mode dance and the same
// "MKD on an existing directory fails" behaviour as the servers people run, and
// it keeps a per-session working directory so a client that relies on the login
// directory being stable is caught.
type fakeFTPServer struct {
	Addr      string
	Root      string
	User      string
	Password  string
	EnableTLS bool

	ln        net.Listener
	tlsConfig *tls.Config
	wg        sync.WaitGroup

	mu   sync.Mutex
	cmds []string
}

func startFakeFTPServer(t *testing.T, root string, enableTLS bool) *fakeFTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &fakeFTPServer{Addr: ln.Addr().String(), Root: root, User: "tester", Password: "secret", EnableTLS: enableTLS, ln: ln}
	if enableTLS {
		srv.tlsConfig = &tls.Config{Certificates: []tls.Certificate{testCertificate(t)}}
	}
	srv.wg.Add(1)
	go func() {
		defer srv.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			srv.wg.Add(1)
			go func() {
				defer srv.wg.Done()
				srv.serve(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		srv.wg.Wait()
	})
	return srv
}

func testCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "servy-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func (s *fakeFTPServer) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.cmds...)
}

func (s *fakeFTPServer) serve(conn net.Conn) {
	defer conn.Close()
	cwd := ""
	raw := conn
	secure := false
	protP := false
	var dataCh chan net.Conn

	send := func(format string, args ...interface{}) {
		fmt.Fprintf(conn, format+"\r\n", args...)
	}
	send("220 servy test FTP ready")
	reader := bufio.NewReader(conn)

	resolve := func(p string) string {
		p = strings.TrimSpace(p)
		if p == "" {
			p = cwd
		} else if !strings.HasPrefix(p, "/") {
			if cwd != "" {
				p = cwd + "/" + p
			}
		}
		clean := filepath.Clean("/" + strings.TrimPrefix(p, "/"))
		return filepath.Join(s.Root, filepath.FromSlash(clean))
	}

	// Real servers accept the passive data connection as soon as the client
	// opens it (the TLS handshake on the data connection may even happen before
	// the transfer command is read), so the accept runs in the background here.
	listenPassive := func() (int, bool) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, false
		}
		dataCh = make(chan net.Conn, 1)
		go func() {
			defer ln.Close()
			ln.(*net.TCPListener).SetDeadline(time.Now().Add(15 * time.Second))
			accepted, err := ln.Accept()
			if err != nil {
				close(dataCh)
				return
			}
			if secure && protP {
				tlsConn := tls.Server(accepted, s.tlsConfig)
				if err := tlsConn.Handshake(); err != nil {
					accepted.Close()
					close(dataCh)
					return
				}
				dataCh <- tlsConn
				return
			}
			dataCh <- accepted
		}()
		return ln.Addr().(*net.TCPAddr).Port, true
	}

	openData := func() net.Conn {
		if dataCh == nil {
			return nil
		}
		select {
		case data, ok := <-dataCh:
			dataCh = nil
			if !ok {
				return nil
			}
			return data
		case <-time.After(15 * time.Second):
			dataCh = nil
			return nil
		}
	}

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		command := strings.ToUpper(parts[0])
		arg := ""
		if len(parts) > 1 {
			arg = parts[1]
		}
		s.mu.Lock()
		s.cmds = append(s.cmds, command)
		s.mu.Unlock()

		switch command {
		case "USER":
			send("331 password required")
		case "PASS":
			if arg == s.Password {
				send("230 logged in")
			} else {
				send("530 bad password")
			}
		case "FEAT":
			send("211-Features:")
			send(" UTF8")
			send(" MLSD")
			send(" EPSV")
			send(" SIZE")
			send("211 End")
		case "OPTS":
			send("200 ok")
		case "TYPE":
			send("200 type set")
		case "PWD":
			send("257 \"/%s\" is the current directory", cwd)
		case "AUTH":
			if !s.EnableTLS || !strings.EqualFold(arg, "TLS") {
				send("502 not supported")
				continue
			}
			send("234 proceed")
			// The ClientHello usually rides along with the AUTH command bytes,
			// so the buffered reader has to stay in front of the TLS layer.
			tlsConn := tls.Server(fakeBufferedConn{Conn: raw, r: reader}, s.tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			raw = tlsConn
			conn = tlsConn
			reader = bufio.NewReader(conn)
			send = func(format string, args ...interface{}) {
				fmt.Fprintf(conn, format+"\r\n", args...)
			}
			secure = true
		case "PBSZ":
			send("200 PBSZ set")
		case "PROT":
			if strings.EqualFold(arg, "P") {
				protP = true
			}
			send("200 protection set")
		case "EPSV":
			port, ok := listenPassive()
			if !ok {
				send("425 cannot open data connection")
				continue
			}
			send("229 Entering Extended Passive Mode (|||%d|)", port)
		case "PASV":
			port, ok := listenPassive()
			if !ok {
				send("425 cannot open data connection")
				continue
			}
			send("227 Entering Passive Mode (127,0,0,1,%d,%d)", port/256, port%256)
		case "MKD":
			target := resolve(arg)
			if err := os.Mkdir(target, 0o755); err != nil {
				send("550 create directory failed")
				continue
			}
			send("257 \"%s\" created", arg)
		case "CWD":
			target := resolve(arg)
			st, err := os.Stat(target)
			if err != nil || !st.IsDir() {
				send("550 no such directory")
				continue
			}
			cwd = strings.TrimPrefix(filepath.ToSlash(target), filepath.ToSlash(s.Root))
			cwd = strings.TrimPrefix(cwd, "/")
			send("250 directory changed")
		case "STOR":
			target := resolve(arg)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				send("550 cannot create parent")
				continue
			}
			send("150 opening data connection")
			data := openData()
			if data == nil {
				send("425 no data connection")
				continue
			}
			f, err := os.Create(target)
			if err != nil {
				data.Close()
				send("550 cannot create file")
				continue
			}
			io.Copy(f, data)
			f.Close()
			data.Close()
			send("226 transfer complete")
		case "RETR":
			target := resolve(arg)
			f, err := os.Open(target)
			if err != nil {
				send("550 no such file")
				continue
			}
			send("150 opening data connection")
			data := openData()
			if data == nil {
				f.Close()
				send("425 no data connection")
				continue
			}
			io.Copy(data, f)
			f.Close()
			data.Close()
			send("226 transfer complete")
		case "SIZE":
			st, err := os.Stat(resolve(arg))
			if err != nil {
				send("550 no such file")
				continue
			}
			send("213 %d", st.Size())
		case "DELE":
			if err := os.Remove(resolve(arg)); err != nil {
				send("550 delete failed")
				continue
			}
			send("250 deleted")
		case "MLSD", "LIST":
			target := resolve(arg)
			entries, err := os.ReadDir(target)
			if err != nil {
				send("550 no such directory")
				continue
			}
			send("150 opening data connection")
			data := openData()
			if data == nil {
				send("425 no data connection")
				continue
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
			for _, e := range entries {
				info, err := e.Info()
				if err != nil {
					continue
				}
				if command == "MLSD" {
					kind := "file"
					if e.IsDir() {
						kind = "dir"
					}
					fmt.Fprintf(data, "type=%s;size=%d;modify=%s; %s\r\n",
						kind, info.Size(), info.ModTime().UTC().Format("20060102150405"), e.Name())
				} else {
					mode := "-rw-r--r--"
					if e.IsDir() {
						mode = "drwxr-xr-x"
					}
					fmt.Fprintf(data, "%s 1 owner group %12d %s %s\r\n",
						mode, info.Size(), info.ModTime().Format("Jan _2 15:04"), e.Name())
				}
			}
			data.Close()
			send("226 transfer complete")
		case "QUIT":
			send("221 bye")
			return
		default:
			send("502 command not implemented")
		}
	}
}

// fakeBufferedConn keeps buffered control-channel bytes readable after the TLS
// upgrade.
type fakeBufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c fakeBufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

/* --------------------------------- tests ---------------------------------- */

func fakeFTPDest(srv *fakeFTPServer, prefix string, extra map[string]string) Destination {
	host, port, _ := net.SplitHostPort(srv.Addr)
	settings := map[string]string{
		"host": host, "port": port, "username": srv.User, "password": srv.Password,
	}
	for k, v := range extra {
		settings[k] = v
	}
	return Destination{Kind: KindFTP, Prefix: prefix, Settings: settings}
}

func TestFTPStoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	srv := startFakeFTPServer(t, root, false)
	store, err := Open(fakeFTPDest(srv, "archives/panel", nil))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()

	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	key := Key("cluster-7f3a", "srv-42", "srv-42_backup.zip")
	payload := []byte(strings.Repeat("ftp-payload-", 4000))
	if err := store.Put(ctx, key, strings.NewReader(string(payload)), int64(len(payload))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Ping and Put both run mkdirs; the session directory must not have moved.
	onDisk := filepath.Join(root, "archives/panel", "cluster-7f3a", "srv-42", "srv-42_backup.zip")
	got, err := os.ReadFile(onDisk)
	if err != nil {
		t.Fatalf("uploaded file missing: %v (recorded commands: %v)", err, srv.recorded())
	}
	if string(got) != string(payload) {
		t.Fatalf("content mismatch")
	}

	rc, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	back, _ := io.ReadAll(rc)
	rc.Close()
	if string(back) != string(payload) {
		t.Fatalf("download mismatch: %d bytes", len(back))
	}

	objects, err := store.List(ctx, ClusterPrefix("cluster-7f3a"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objects) != 1 || objects[0].Key != key || objects[0].Size != int64(len(payload)) {
		t.Fatalf("unexpected listing: %+v", objects)
	}
	// A different cluster must not see these archives.
	other, err := store.List(ctx, ClusterPrefix("cluster-b"))
	if err != nil {
		t.Fatalf("List other: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("cluster isolation broken: %+v", other)
	}

	if _, err := store.Get(ctx, Key("cluster-other", "srv-9", "nope.zip")); err != ErrNotExist {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}

	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(onDisk); !os.IsNotExist(err) {
		t.Fatalf("file still present after delete")
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestFTPFTPSEsplicit(t *testing.T) {
	root := t.TempDir()
	srv := startFakeFTPServer(t, root, true)
	store, err := Open(fakeFTPDest(srv, "secure", map[string]string{"tls": "explicit", "insecure": "true"}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping over FTPS: %v", err)
	}
	key := Key("c1", "s1", "file.zip")
	payload := []byte("encrypted payload")
	if err := store.Put(ctx, key, strings.NewReader(string(payload)), int64(len(payload))); err != nil {
		t.Fatalf("Put over FTPS: %v", err)
	}
	rc, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get over FTPS: %v", err)
	}
	back, _ := io.ReadAll(rc)
	rc.Close()
	if string(back) != string(payload) {
		t.Fatalf("FTPS content mismatch: %q", back)
	}
}

func TestFTPBadCredentials(t *testing.T) {
	root := t.TempDir()
	srv := startFakeFTPServer(t, root, false)
	dest := fakeFTPDest(srv, "", nil)
	dest.Settings["password"] = "wrong"
	store, err := Open(dest)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Ping(context.Background()); err == nil {
		t.Fatal("expected login to fail")
	}
}

func TestFTPUnreachableServer(t *testing.T) {
	dest := Destination{Kind: KindFTP, Settings: map[string]string{
		"host": "127.0.0.1", "port": freePort(t), "username": "u", "password": "p",
	}}
	store, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err == nil {
		t.Fatal("expected connection refused")
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

// TestFTPExternalServer runs the round trip against a real FTP/FTPS server when
// SERVY_TEST_FTP_ADDR is set (CI can point it at a throwaway pyftpdlib or
// vsftpd instance).
func TestFTPExternalServer(t *testing.T) {
	addr := os.Getenv("SERVY_TEST_FTP_ADDR")
	if addr == "" {
		t.Skip("SERVY_TEST_FTP_ADDR not set")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("bad SERVY_TEST_FTP_ADDR: %v", err)
	}
	settings := map[string]string{
		"host": host, "port": port,
		"username": os.Getenv("SERVY_TEST_FTP_USER"),
		"password": os.Getenv("SERVY_TEST_FTP_PASSWORD"),
		"tls":      os.Getenv("SERVY_TEST_FTP_TLS"),
		"insecure": os.Getenv("SERVY_TEST_FTP_INSECURE"),
	}
	store, err := Open(Destination{Kind: KindFTP, Prefix: "servy-test", Settings: settings})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	key := Key("cluster-external", "srv-x", "roundtrip.zip")
	payload := []byte("external ftp round trip")
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
		t.Fatal("listing is empty")
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
