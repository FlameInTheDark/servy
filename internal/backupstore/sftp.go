package backupstore

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// knownHostsFile is where SFTP host keys accepted on first use are remembered.
// The panel sets it to <data>/config/known_hosts at startup.
var (
	knownHostsMu   sync.RWMutex
	knownHostsFile string
)

// SetKnownHostsFile points the SFTP backend at the file used to pin host keys
// on first use. Without a file (and without a pinned key) SFTP uploads are
// refused instead of trusting an unknown host silently.
func SetKnownHostsFile(path string) {
	knownHostsMu.Lock()
	defer knownHostsMu.Unlock()
	knownHostsFile = path
}

func currentKnownHostsFile() string {
	knownHostsMu.RLock()
	defer knownHostsMu.RUnlock()
	return knownHostsFile
}

// sftpStore uploads archives over SSH to an SFTP server.
type sftpStore struct {
	host       string
	port       int
	username   string
	password   string
	privateKey string
	passphrase string
	hostKey    string
	prefix     string
}

func newSFTPStore(settings map[string]string, prefix string) (*sftpStore, error) {
	port := 22
	if v := strings.TrimSpace(settings["port"]); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p <= 0 || p > 65535 {
			return nil, fmt.Errorf("invalid SFTP port %q", v)
		}
		port = p
	}
	if strings.TrimSpace(settings["private_key"]) == "" && settings["password"] == "" {
		return nil, fmt.Errorf("SFTP needs a password or a private key")
	}
	return &sftpStore{
		host:       strings.TrimSpace(settings["host"]),
		port:       port,
		username:   strings.TrimSpace(settings["username"]),
		password:   settings["password"],
		privateKey: settings["private_key"],
		passphrase: settings["passphrase"],
		hostKey:    strings.TrimSpace(settings["host_key"]),
		prefix:     prefix,
	}, nil
}

func (s *sftpStore) Kind() string { return KindSFTP }

func (s *sftpStore) Location() string {
	loc := fmt.Sprintf("sftp://%s@%s:%d", s.username, s.host, s.port)
	if s.prefix != "" {
		loc += "/" + s.prefix
	}
	return loc
}

// hostKeyCallback pins the host key: an explicitly configured key always wins,
// otherwise keys are remembered in the known-hosts file on first use.
func (s *sftpStore) hostKeyCallback() (ssh.HostKeyCallback, error) {
	if s.hostKey != "" {
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s.hostKey))
		if err != nil {
			// Also accept a bare base64 blob.
			key, err = ssh.ParsePublicKey([]byte(s.hostKey))
			if err != nil {
				return nil, fmt.Errorf("invalid pinned host key: %w", err)
			}
		}
		return ssh.FixedHostKey(key), nil
	}
	file := currentKnownHostsFile()
	if file == "" {
		return nil, fmt.Errorf("no host key pinned for %s and no known-hosts file configured", s.host)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(file); os.IsNotExist(err) {
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			return nil, err
		}
	}
	verify, err := knownhosts.New(file)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", file, err)
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := verify(hostname, remote, key)
		if err == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if ok := asKeyError(err, &keyErr); ok && len(keyErr.Want) == 0 {
			// First time we see this host: remember the key (trust on first use).
			line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key) + "\n"
			f, ferr := os.OpenFile(file, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
			if ferr != nil {
				return ferr
			}
			defer f.Close()
			if _, werr := f.WriteString(line); werr != nil {
				return werr
			}
			return nil
		}
		return fmt.Errorf("host key verification for %s failed: %w", hostname, err)
	}, nil
}

func asKeyError(err error, target **knownhosts.KeyError) bool {
	if ke, ok := err.(*knownhosts.KeyError); ok {
		*target = ke
		return true
	}
	return false
}

func (s *sftpStore) authMethods() ([]ssh.AuthMethod, error) {
	methods := []ssh.AuthMethod{}
	if strings.TrimSpace(s.privateKey) != "" {
		var (
			signer ssh.Signer
			err    error
		)
		if s.passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(s.privateKey), []byte(s.passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(s.privateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("cannot use the private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if s.password != "" {
		methods = append(methods, ssh.Password(s.password))
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("SFTP needs a password or a private key")
	}
	return methods, nil
}

// withWire opens an SSH connection, runs fn and closes everything after.
func (s *sftpStore) withWire(ctx context.Context, fn func(*sftpWire) error) error {
	callback, err := s.hostKeyCallback()
	if err != nil {
		return err
	}
	methods, err := s.authMethods()
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(s.host, strconv.Itoa(s.port))
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", addr, err)
	}
	clientConfig := &ssh.ClientConfig{
		User:            s.username,
		Auth:            methods,
		HostKeyCallback: callback,
		Timeout:         20 * time.Second,
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientConfig)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SSH handshake with %s failed: %w", addr, err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	wire, err := newSFTPWire(client)
	if err != nil {
		return err
	}
	defer wire.Close()

	done := make(chan struct{})
	defer close(done)
	if deadline, ok := ctx.Deadline(); ok {
		go func() {
			select {
			case <-done:
			case <-time.After(time.Until(deadline)):
				client.Close()
			}
		}()
	}
	return fn(wire)
}

func (s *sftpStore) keyWithPrefix(key string) string {
	clean := strings.Join(splitKey(key), "/")
	if s.prefix == "" {
		return clean
	}
	if clean == "" {
		return s.prefix
	}
	return s.prefix + "/" + clean
}

func (s *sftpStore) mkdirs(wire *sftpWire, dir string) error {
	if dir == "" {
		return nil
	}
	current := ""
	if strings.HasPrefix(dir, "/") {
		current = "/"
	}
	for _, part := range splitKey(dir) {
		switch {
		case current == "":
			current = part
		case current == "/":
			current += part
		default:
			current += "/" + part
		}
		// SSH_FX_FAILURE is what servers report for "already exists" (the v3
		// protocol has no dedicated status), so creation is best effort and the
		// directory is verified afterwards.
		_ = wire.mkdir(current)
	}
	handle, err := wire.opendir(current)
	if err != nil {
		return err
	}
	return wire.close(handle)
}

func (s *sftpStore) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	full := s.keyWithPrefix(key)
	return s.withWire(ctx, func(wire *sftpWire) error {
		if err := s.mkdirs(wire, path.Dir(full)); err != nil {
			return err
		}
		handle, err := wire.open(full, fxfWrite|fxfCreat|fxfTrunc, 0o644)
		if err != nil {
			return err
		}
		buf := make([]byte, maxRead)
		var offset uint64
		for {
			n, rerr := io.ReadFull(body, buf)
			if n > 0 {
				if werr := wire.write(handle, offset, buf[:n]); werr != nil {
					wire.close(handle)
					return werr
				}
				offset += uint64(n)
			}
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				break
			}
			if rerr != nil {
				wire.close(handle)
				return rerr
			}
		}
		if size >= 0 && int64(offset) != size {
			wire.close(handle)
			return fmt.Errorf("short upload: wrote %d of %d bytes", offset, size)
		}
		return wire.close(handle)
	})
}

// sftpReader streams a remote file, fetching it in windows.
type sftpReader struct {
	wire   *sftpWire
	handle string
	offset uint64
	buf    []byte
	eof    bool
}

func (r *sftpReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		if r.eof {
			return 0, io.EOF
		}
		chunk, err := r.wire.read(r.handle, r.offset, maxRead)
		if err == io.EOF {
			r.eof = true
			return 0, io.EOF
		}
		if err != nil {
			return 0, err
		}
		if len(chunk) == 0 {
			r.eof = true
			return 0, io.EOF
		}
		r.buf = chunk
		r.offset += uint64(len(chunk))
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *sftpReader) Close() error { return r.wire.close(r.handle) }

func (s *sftpStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	full := s.keyWithPrefix(key)
	// The connection has to outlive this call, so the wire is not closed here.
	callback, err := s.hostKeyCallback()
	if err != nil {
		return nil, err
	}
	methods, err := s.authMethods()
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(s.host, strconv.Itoa(s.port))
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", addr, err)
	}
	clientConfig := &ssh.ClientConfig{
		User: s.username, Auth: methods, HostKeyCallback: callback, Timeout: 20 * time.Second,
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientConfig)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SSH handshake with %s failed: %w", addr, err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	wire, err := newSFTPWire(client)
	if err != nil {
		client.Close()
		return nil, err
	}
	handle, err := wire.open(full, fxfRead, 0)
	if err != nil {
		client.Close()
		if isSFTPNotExist(err) {
			return nil, ErrNotExist
		}
		return nil, err
	}
	return &sftpRemoteReader{inner: &sftpReader{wire: wire, handle: handle}, client: client}, nil
}

// sftpRemoteReader ties the file handle and the SSH connection together.
type sftpRemoteReader struct {
	inner  *sftpReader
	client *ssh.Client
}

func (r *sftpRemoteReader) Read(p []byte) (int, error) { return r.inner.Read(p) }

func (r *sftpRemoteReader) Close() error {
	r.inner.Close()
	return r.client.Close()
}

func (s *sftpStore) Delete(ctx context.Context, key string) error {
	full := s.keyWithPrefix(key)
	return s.withWire(ctx, func(wire *sftpWire) error {
		if err := wire.remove(full); err != nil {
			if isSFTPNotExist(err) {
				return nil
			}
			return err
		}
		// prune empty parent directories inside our own tree
		dir := path.Dir(full)
		prefix := s.prefix
		for depth := 0; depth < 8 && dir != "." && dir != "/" && dir != prefix; depth++ {
			handle, err := wire.opendir(dir)
			if err != nil {
				break
			}
			entries, err := wire.readdir(handle)
			wire.close(handle)
			if err != nil && err != io.EOF {
				break
			}
			if len(entries) > 0 {
				break
			}
			if err := wire.removeDir(dir); err != nil {
				break
			}
			dir = path.Dir(dir)
		}
		return nil
	})
}

// removeDir deletes an empty directory.
func (c *sftpWire) removeDir(path string) error {
	typ, payload, err := c.request(fxpRmdir, func(p *packet) { p.str(path) })
	if err != nil {
		return err
	}
	if typ == fxpStatus {
		return statusError("rmdir "+path, payload)
	}
	return nil
}

func (s *sftpStore) List(ctx context.Context, prefix string) ([]Object, error) {
	full := s.keyWithPrefix(prefix)
	out := []Object{}
	err := s.withWire(ctx, func(wire *sftpWire) error {
		return s.walk(wire, full, prefix, 0, func(rel string, size int64, modified time.Time) {
			out = append(out, Object{Key: rel, Size: size, Modified: modified})
		})
	})
	if err != nil {
		if isSFTPNotExist(err) {
			return []Object{}, nil
		}
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *sftpStore) walk(wire *sftpWire, dir, keyPrefix string, depth int, report func(string, int64, time.Time)) error {
	if depth > 12 {
		return nil
	}
	handle, err := wire.opendir(dir)
	if err != nil {
		if isSFTPNotExist(err) {
			return nil
		}
		return err
	}
	defer wire.close(handle)
	for {
		entries, err := wire.readdir(handle)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			if isSFTPNotExist(err) {
				return nil
			}
			return err
		}
		for _, entry := range entries {
			child := dir
			if child != "" && !strings.HasSuffix(child, "/") {
				child += "/"
			}
			child += entry.Name
			if entry.Dir {
				if err := s.walk(wire, child, keyPrefix, depth+1, report); err != nil {
					return err
				}
				continue
			}
			rel, ok := relKey(child, s.prefix)
			if !ok {
				continue
			}
			if keyPrefix != "" && !strings.HasPrefix(rel, keyPrefix) {
				continue
			}
			report(rel, entry.Size, time.Unix(int64(entry.Modified), 0).UTC())
		}
	}
}

func (s *sftpStore) Ping(ctx context.Context) error {
	return s.withWire(ctx, func(wire *sftpWire) error {
		if err := s.mkdirs(wire, s.prefix); err != nil {
			return err
		}
		if _, err := wire.realpath("."); err != nil {
			return err
		}
		return nil
	})
}
