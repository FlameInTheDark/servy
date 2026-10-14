package backupstore

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// ftpStore uploads archives to an FTP or FTPS (explicit/implicit TLS) server.
type ftpStore struct {
	host      string
	port      int
	username  string
	password  string
	tlsMode   string // none | explicit | implicit
	insecure  bool
	prefix    string
	tlsConfig *tls.Config
}

func newFTPStore(settings map[string]string, prefix string) (*ftpStore, error) {
	port := 21
	if v := strings.TrimSpace(settings["port"]); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p <= 0 || p > 65535 {
			return nil, fmt.Errorf("invalid FTP port %q", v)
		}
		port = p
	}
	mode := strings.TrimSpace(settings["tls"])
	if mode == "" {
		mode = "none"
	}
	if mode == "implicit" && strings.TrimSpace(settings["port"]) == "" {
		port = 990
	}
	return &ftpStore{
		host:     strings.TrimSpace(settings["host"]),
		port:     port,
		username: strings.TrimSpace(settings["username"]),
		password: settings["password"],
		tlsMode:  mode,
		insecure: strings.EqualFold(settings["insecure"], "true"),
		prefix:   prefix,
		tlsConfig: &tls.Config{
			ServerName:         strings.TrimSpace(settings["host"]),
			InsecureSkipVerify: strings.EqualFold(settings["insecure"], "true"),
		},
	}, nil
}

func (s *ftpStore) Kind() string { return KindFTP }

func (s *ftpStore) Location() string {
	scheme := "ftp"
	if s.tlsMode != "none" {
		scheme = "ftps"
	}
	loc := fmt.Sprintf("%s://%s:%d", scheme, s.host, s.port)
	if s.prefix != "" {
		loc += "/" + s.prefix
	}
	return loc
}

func (s *ftpStore) keyWithPrefix(key string) string {
	clean := strings.Join(splitKey(key), "/")
	if s.prefix == "" {
		return clean
	}
	if clean == "" {
		return s.prefix
	}
	return s.prefix + "/" + clean
}

func (s *ftpStore) dial(ctx context.Context) (*ftpConn, error) {
	addr := net.JoinHostPort(s.host, strconv.Itoa(s.port))
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", addr, err)
	}
	conn := net.Conn(&idleConn{Conn: raw, timeout: ftpIdleTimeout})
	if s.tlsMode == "implicit" {
		tlsConn := tls.Client(conn, s.tlsConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("TLS handshake with %s: %w", addr, err)
		}
		conn = tlsConn
	}
	c := newFTPConn(conn, s.tlsConfig)
	code, msg, err := c.readResponse()
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("greeting from %s: %w", addr, err)
	}
	if code != 220 {
		c.Close()
		return nil, fmt.Errorf("server %s refused the connection: %d %s", addr, code, msg)
	}
	if s.tlsMode == "explicit" {
		if err := c.startTLS(ctx); err != nil {
			c.Close()
			return nil, err
		}
	}
	if err := c.login(s.username, s.password); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// withConn runs fn on a freshly logged in connection and always closes it.
func (s *ftpStore) withConn(ctx context.Context, fn func(*ftpConn) error) error {
	c, err := s.dial(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := fn(c); err != nil {
		return err
	}
	c.cmd("QUIT")
	return nil
}

func (s *ftpStore) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	full := s.keyWithPrefix(key)
	return s.withConn(ctx, func(c *ftpConn) error {
		if err := c.mkdirs(ctx, dirOf(full)); err != nil {
			return err
		}
		data, err := c.openData(ctx)
		if err != nil {
			return err
		}
		code, msg, err := c.cmd("STOR " + full)
		if err != nil {
			data.Close()
			return err
		}
		if code != 150 && code != 125 {
			data.Close()
			return fmt.Errorf("ftp STOR %s refused: %d %s", full, code, msg)
		}
		if _, err := io.Copy(data, body); err != nil {
			data.Close()
			c.cmd("ABOR")
			return fmt.Errorf("uploading %s: %w", full, err)
		}
		if err := data.Close(); err != nil {
			return fmt.Errorf("finishing upload of %s: %w", full, err)
		}
		if code, msg, err := c.readResponse(); err != nil {
			return err
		} else if code >= 400 {
			return fmt.Errorf("ftp STOR %s failed: %d %s", full, code, msg)
		}
		return nil
	})
}

func (s *ftpStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	full := s.keyWithPrefix(key)
	c, err := s.dial(ctx)
	if err != nil {
		return nil, err
	}
	data, err := c.openData(ctx)
	if err != nil {
		c.Close()
		return nil, err
	}
	code, msg, err := c.cmd("RETR " + full)
	if err != nil {
		data.Close()
		c.Close()
		return nil, err
	}
	if code == 550 {
		data.Close()
		c.Close()
		return nil, ErrNotExist
	}
	if code != 150 && code != 125 {
		data.Close()
		c.Close()
		return nil, fmt.Errorf("ftp RETR %s refused: %d %s", full, code, msg)
	}
	return &ftpReader{data: data, ctrl: c}, nil
}

// ftpReader streams a download and releases the control connection when done.
type ftpReader struct {
	data io.ReadCloser
	ctrl *ftpConn
	done bool
}

func (r *ftpReader) Read(p []byte) (int, error) { return r.data.Read(p) }

func (r *ftpReader) Close() error {
	if r.done {
		return nil
	}
	r.done = true
	io.Copy(io.Discard, io.LimitReader(r.data, 1<<20))
	r.data.Close()
	if r.ctrl != nil {
		r.ctrl.readResponse()
		r.ctrl.cmd("QUIT")
		r.ctrl.Close()
	}
	return nil
}

func (s *ftpStore) Delete(ctx context.Context, key string) error {
	full := s.keyWithPrefix(key)
	return s.withConn(ctx, func(c *ftpConn) error {
		code, _, err := c.cmd("DELE " + full)
		if err != nil {
			return err
		}
		if code >= 400 && code != 550 { // 550 = already gone
			return fmt.Errorf("ftp DELE %s failed: %d", full, code)
		}
		return nil
	})
}

func (s *ftpStore) List(ctx context.Context, prefix string) ([]Object, error) {
	full := s.keyWithPrefix(prefix)
	out := []Object{}
	err := s.withConn(ctx, func(c *ftpConn) error {
		return c.walk(ctx, full, prefix, s.prefix, 0, func(rel string, size int64, modified time.Time) {
			out = append(out, Object{Key: rel, Size: size, Modified: modified})
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ftpStore) Ping(ctx context.Context) error {
	return s.withConn(ctx, func(c *ftpConn) error {
		if err := c.mkdirs(ctx, s.prefix); err != nil {
			return err
		}
		code, msg, err := c.cmd("PWD")
		if err != nil {
			return err
		}
		if code >= 400 {
			return fmt.Errorf("ftp PWD failed: %d %s", code, msg)
		}
		return nil
	})
}

func dirOf(key string) string {
	i := strings.LastIndex(key, "/")
	if i < 0 {
		return ""
	}
	return key[:i]
}

/* ------------------------------ FTP client -------------------------------- */

// ftpIdleTimeout aborts a transfer that made no progress for this long.
const ftpIdleTimeout = 5 * time.Minute

// idleConn resets the connection deadline before every read and write so a
// stalled server cannot block a backup forever while long transfers stay fine.
type idleConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleConn) Read(p []byte) (int, error) {
	c.Conn.SetReadDeadline(time.Now().Add(c.timeout))
	return c.Conn.Read(p)
}

func (c *idleConn) Write(p []byte) (int, error) {
	c.Conn.SetWriteDeadline(time.Now().Add(c.timeout))
	return c.Conn.Write(p)
}

// bufferedConn feeds a reader's already buffered bytes into the connection, so
// a TLS upgrade does not lose data the reader had read ahead (the ClientHello
// or ServerHello often arrives in the same TCP segment as the preceding reply).
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// ftpConn is a small FTP control connection: enough of RFC 959 plus MLSD,
// EPSV, and AUTH TLS to store and fetch files.
type ftpConn struct {
	conn          net.Conn
	r             *bufio.Reader
	tlsConfig     *tls.Config
	features      map[string]bool
	protectedData bool
}

func newFTPConn(conn net.Conn, tlsConfig *tls.Config) *ftpConn {
	return &ftpConn{
		conn:      conn,
		r:         bufio.NewReaderSize(conn, 16<<10),
		tlsConfig: tlsConfig,
		features:  map[string]bool{},
	}
}

func (c *ftpConn) Close() error { return c.conn.Close() }

// cmd sends a command and reads the (possibly multi line) reply.
func (c *ftpConn) cmd(line string) (int, string, error) {
	if _, err := io.WriteString(c.conn, line+"\r\n"); err != nil {
		return 0, "", err
	}
	return c.readResponse()
}

// cmdf formats a command first; only used for the few commands that take
// user supplied values.
func (c *ftpConn) cmdf(format string, args ...interface{}) (int, string, error) {
	return c.cmd(fmt.Sprintf(format, args...))
}

// readResponse parses one FTP reply, joining continuation lines.
func (c *ftpConn) readResponse() (int, string, error) {
	line, err := c.readLine()
	if err != nil {
		return 0, "", err
	}
	if len(line) < 3 {
		return 0, line, fmt.Errorf("malformed FTP reply %q", line)
	}
	code, err := strconv.Atoi(line[:3])
	if err != nil {
		return 0, line, fmt.Errorf("malformed FTP reply %q", line)
	}
	if len(line) > 3 && line[3] == '-' {
		// multi line: the final line repeats the code followed by a space
		parts := []string{}
		if len(line) > 4 {
			parts = append(parts, line[4:])
		}
		for {
			next, err := c.readLine()
			if err != nil {
				return code, strings.Join(parts, " "), err
			}
			if len(next) > 3 && next[:3] == line[:3] && next[3] == ' ' {
				parts = append(parts, strings.TrimSpace(next[4:]))
				break
			}
			if len(next) >= 3 && next[:3] == line[:3] && len(next) == 3 {
				break
			}
			parts = append(parts, strings.TrimSpace(next))
		}
		return code, strings.Join(parts, " "), nil
	}
	msg := strings.TrimSpace(line[4:])
	if len(line) <= 4 {
		msg = strings.TrimSpace(line[3:])
	}
	return code, msg, nil
}

func (c *ftpConn) readLine() (string, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// startTLS upgrades an explicit FTPS connection.
func (c *ftpConn) startTLS(ctx context.Context) error {
	code, msg, err := c.cmd("AUTH TLS")
	if err != nil {
		return err
	}
	if code != 234 {
		return fmt.Errorf("server refused AUTH TLS: %d %s", code, msg)
	}
	tlsConn := tls.Client(bufferedConn{Conn: c.conn, r: c.r}, c.tlsConfig)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("TLS handshake: %w", err)
	}
	c.conn = tlsConn
	c.r = bufio.NewReaderSize(tlsConn, 16<<10)
	// Data connections have to be protected too; servers require the explicit
	// PBSZ/PROT handshake before the first transfer. Failures are tolerated for
	// servers that only encrypt the control channel.
	if code, _, err := c.cmd("PBSZ 0"); err == nil && code < 400 {
		if code, _, err := c.cmd("PROT P"); err == nil && code < 400 {
			c.protectedData = true
		}
	}
	return nil
}

func (c *ftpConn) login(user, pass string) error {
	if user == "" {
		user = "anonymous"
		if pass == "" {
			pass = "servy@localhost"
		}
	}
	code, msg, err := c.cmdf("USER %s", user)
	if err != nil {
		return err
	}
	switch {
	case code == 230:
		return nil
	case code == 331 || code == 332:
		code, msg, err = c.cmdf("PASS %s", pass)
		if err != nil {
			return err
		}
		if code != 230 && code != 202 {
			return fmt.Errorf("FTP login for %q failed: %d %s", user, code, msg)
		}
	default:
		return fmt.Errorf("FTP USER %q failed: %d %s", user, code, msg)
	}
	// Optional capabilities; ignore failures on servers that lack them.
	if code, _, err := c.cmd("FEAT"); err == nil && code == 211 {
		// FEAT already consumed its own multi line reply
	}
	c.detectFeatures()
	if c.features["utf8"] {
		c.cmd("OPTS UTF8 ON")
	}
	if code, msg, err := c.cmd("TYPE I"); err != nil {
		return err
	} else if code >= 400 {
		return fmt.Errorf("FTP TYPE I failed: %d %s", code, msg)
	}
	return nil
}

// detectFeatures queries FEAT and records the feature keywords.
func (c *ftpConn) detectFeatures() {
	if _, err := io.WriteString(c.conn, "FEAT\r\n"); err != nil {
		return
	}
	line, err := c.readLine()
	if err != nil {
		return
	}
	if !strings.HasPrefix(line, "211") {
		return
	}
	if len(line) > 3 && line[3] == '-' {
		for {
			next, err := c.readLine()
			if err != nil {
				return
			}
			if strings.HasPrefix(next, "211") && (len(next) == 3 || next[3] == ' ') {
				return
			}
			name := strings.TrimSpace(next)
			if i := strings.IndexAny(name, " \t"); i > 0 {
				name = name[:i]
			}
			if name != "" {
				c.features[strings.ToLower(name)] = true
			}
		}
	}
}

// mkdirs creates every missing directory of a path. Directories are created
// level by level because most servers refuse MKD for a path whose parent is
// missing. An existing directory answers with an error (usually 550), which is
// ignored on purpose — the session working directory must stay where the login
// left it, so CWD is never used here.
func (c *ftpConn) mkdirs(ctx context.Context, dir string) error {
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
		if code, _, err := c.cmd("MKD " + current); err != nil {
			return err
		} else if code >= 400 && code != 550 && code != 450 && code != 521 {
			return fmt.Errorf("ftp MKD %s failed: %d", current, code)
		}
	}
	return nil
}

// openData opens a passive mode data connection.
func (c *ftpConn) openData(ctx context.Context) (net.Conn, error) {
	host, port, err := c.epsv(ctx)
	if err != nil {
		host, port, err = c.pasv(ctx)
		if err != nil {
			return nil, err
		}
	}
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil && ctx.Err() == nil {
		// Some servers advertise an unreachable internal address; retry against
		// the control connection's peer, which is what EPSV would have used.
		if peer, ok := c.conn.RemoteAddr().(*net.TCPAddr); ok {
			if alt, altErr := dialer.DialContext(ctx, "tcp", net.JoinHostPort(peer.IP.String(), strconv.Itoa(port))); altErr == nil {
				return alt, nil
			}
		}
		return nil, fmt.Errorf("opening data connection to %s:%d: %w", host, port, err)
	}
	if err != nil {
		return nil, err
	}
	if c.protectedData {
		tlsConn := tls.Client(conn, c.tlsConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("TLS handshake on the data connection: %w", err)
		}
		return tlsConn, nil
	}
	return &idleConn{Conn: conn, timeout: ftpIdleTimeout}, nil
}

// epsv asks for an extended passive mode data port (IPv6 friendly).
func (c *ftpConn) epsv(ctx context.Context) (string, int, error) {
	code, msg, err := c.cmd("EPSV")
	if err != nil {
		return "", 0, err
	}
	if code != 229 {
		return "", 0, fmt.Errorf("EPSV unsupported: %d %s", code, msg)
	}
	open := strings.Index(msg, "(")
	closeIdx := strings.Index(msg, ")")
	if open < 0 || closeIdx <= open {
		return "", 0, fmt.Errorf("malformed EPSV reply %q", msg)
	}
	inner := strings.Trim(msg[open+1:closeIdx], "|")
	port, err := strconv.Atoi(strings.Trim(inner, "|"))
	if err != nil {
		fields := strings.Split(msg[open+1:closeIdx], "|")
		port = 0
		for _, f := range fields {
			if p, err := strconv.Atoi(f); err == nil {
				port = p
			}
		}
		if port == 0 {
			return "", 0, fmt.Errorf("malformed EPSV reply %q", msg)
		}
	}
	peer, ok := c.conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return "", 0, errors.New("unknown FTP peer address")
	}
	return peer.IP.String(), port, nil
}

// pasv asks for a passive mode data port (IPv4 fallback).
func (c *ftpConn) pasv(ctx context.Context) (string, int, error) {
	code, msg, err := c.cmd("PASV")
	if err != nil {
		return "", 0, err
	}
	if code != 227 {
		return "", 0, fmt.Errorf("PASV failed: %d %s", code, msg)
	}
	open := strings.Index(msg, "(")
	closeIdx := strings.Index(msg, ")")
	if open < 0 || closeIdx <= open {
		return "", 0, fmt.Errorf("malformed PASV reply %q", msg)
	}
	fields := strings.Split(msg[open+1:closeIdx], ",")
	if len(fields) != 6 {
		return "", 0, fmt.Errorf("malformed PASV reply %q", msg)
	}
	nums := make([]int, 6)
	for i, f := range fields {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			return "", 0, fmt.Errorf("malformed PASV reply %q", msg)
		}
		nums[i] = n
	}
	host := fmt.Sprintf("%d.%d.%d.%d", nums[0], nums[1], nums[2], nums[3])
	port := nums[4]*256 + nums[5]
	return host, port, nil
}

// walk lists a directory tree, reporting files below keyPrefix.
func (c *ftpConn) walk(ctx context.Context, dir, keyPrefix, destPrefix string, depth int, report func(rel string, size int64, modified time.Time)) error {
	if depth > 12 {
		return nil
	}
	entries, err := c.list(ctx, dir)
	if err != nil {
		if isNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		child := dir
		if child != "" && !strings.HasSuffix(child, "/") {
			child += "/"
		}
		child += e.Name
		if e.Dir {
			if err := c.walk(ctx, child, keyPrefix, destPrefix, depth+1, report); err != nil {
				return err
			}
			continue
		}
		rel, ok := relKey(child, destPrefix)
		if !ok {
			continue
		}
		if keyPrefix != "" && !strings.HasPrefix(rel, keyPrefix) {
			continue
		}
		report(rel, e.Size, e.Modified)
	}
	return nil
}

type ftpEntry struct {
	Name     string
	Size     int64
	Modified time.Time
	Dir      bool
}

// list returns the entries of one directory using MLSD when available.
func (c *ftpConn) list(ctx context.Context, dir string) ([]ftpEntry, error) {
	data, err := c.openData(ctx)
	if err != nil {
		return nil, err
	}
	useMLSD := c.features["mlsd"]
	command := "LIST"
	if useMLSD {
		command = "MLSD"
	}
	if dir != "" && dir != "." {
		command += " " + dir
	}
	code, msg, err := c.cmd(command)
	if err != nil {
		data.Close()
		return nil, err
	}
	if code != 150 && code != 125 {
		data.Close()
		if code == 550 {
			// The 550 reply is the complete answer; the server sends nothing else.
			return nil, fmt.Errorf("%w: %s", ErrNotExist, msg)
		}
		return nil, fmt.Errorf("ftp %s %s failed: %d %s", command, dir, code, msg)
	}
	raw, err := io.ReadAll(io.LimitReader(data, 32<<20))
	data.Close()
	if err != nil {
		return nil, err
	}
	if code, _, err := c.readResponse(); err != nil {
		return nil, err
	} else if code >= 400 {
		return nil, fmt.Errorf("ftp %s %s failed: %d", command, dir, code)
	}
	out := []ftpEntry{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if useMLSD {
			if e, ok := parseMLSD(line); ok {
				out = append(out, e)
			}
			continue
		}
		if e, ok := parseUnixList(line); ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// parseMLSD parses one machine readable directory listing line.
func parseMLSD(line string) (ftpEntry, bool) {
	space := strings.Index(line, " ")
	if space < 0 {
		return ftpEntry{}, false
	}
	facts, name := line[:space], line[space+1:]
	if name == "." || name == ".." {
		return ftpEntry{}, false
	}
	e := ftpEntry{Name: name}
	for _, fact := range strings.Split(facts, ";") {
		switch {
		case fact == "type=dir" || fact == "type=cdir" || fact == "type=pdir":
			e.Dir = true
		case strings.HasPrefix(fact, "type=file"):
			e.Dir = false
		case strings.HasPrefix(fact, "size="):
			e.Size, _ = strconv.ParseInt(strings.TrimPrefix(fact, "size="), 10, 64)
		case strings.HasPrefix(fact, "modify="):
			value := strings.TrimPrefix(fact, "modify=")
			if len(value) >= 14 {
				if t, err := time.Parse("20060102150405", value[:14]); err == nil {
					e.Modified = t.UTC()
				}
			}
		}
	}
	return e, !e.Dir || true
}

// parseUnixList is a best effort parser for the classic LIST format.
func parseUnixList(line string) (ftpEntry, bool) {
	fields := strings.Fields(line)
	if len(fields) < 9 {
		return ftpEntry{}, false
	}
	perms := fields[0]
	if len(perms) < 10 {
		return ftpEntry{}, false
	}
	name := strings.Join(fields[8:], " ")
	// Skip the symlink target part ("name -> target").
	if i := strings.Index(name, " -> "); i >= 0 {
		name = name[:i]
	}
	if name == "." || name == ".." {
		return ftpEntry{}, false
	}
	e := ftpEntry{Name: name, Dir: perms[0] == 'd'}
	if !e.Dir {
		e.Size, _ = strconv.ParseInt(fields[4], 10, 64)
	}
	// "Mon 02 15:04" (recent) or "Mon 02 2024" (older)
	layout := "Jan 02 15:04"
	stamp := fields[5] + " " + fields[6] + " " + fields[7]
	if len(fields[7]) == 4 {
		layout = "Jan 02 2006"
		stamp = fields[5] + " " + fields[6] + " " + fields[7]
	}
	if t, err := time.Parse(layout, stamp); err == nil {
		e.Modified = t.UTC()
	}
	return e, true
}

func isNotExist(err error) bool { return errors.Is(err, ErrNotExist) }
