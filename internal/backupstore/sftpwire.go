package backupstore

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/ssh"
)

// This file implements the subset of the SFTP protocol version 3
// (draft-ietf-secsh-filexfer-02) that storing and fetching archives needs. It
// runs on top of golang.org/x/crypto/ssh, which the panel already uses, so no
// extra module is pulled in for SFTP support.

const (
	fxpInit     = 1
	fxpVersion  = 2
	fxpOpen     = 3
	fxpClose    = 4
	fxpRead     = 5
	fxpWrite    = 6
	fxpLstat    = 7
	fxpFstat    = 8
	fxpSetstat  = 9
	fxpOpendir  = 11
	fxpReaddir  = 12
	fxpRemove   = 13
	fxpMkdir    = 14
	fxpRmdir    = 15
	fxpRealpath = 16
	fxpStat     = 17
	fxpRename   = 18
	fxpStatus   = 101
	fxpHandle   = 102
	fxpData     = 103
	fxpName     = 104
	fxpAttrs    = 105
)

const (
	fxOK               = 0
	fxEOF              = 1
	fxNoSuchFile       = 2
	fxPermissionDenied = 3
	fxFailure          = 4
	fxBadMessage       = 5
	fxOpUnsupported    = 8
)

// attribute flags
const (
	attrSize        = 0x00000001
	attrUIDGID      = 0x00000002
	attrPermissions = 0x00000004
	attrACModTime   = 0x00000008
	attrExtended    = 0x80000000
)

// open flags
const (
	fxfRead   = 0x00000001
	fxfWrite  = 0x00000002
	fxfCreat  = 0x00000008
	fxfTrunc  = 0x00000010
	fxfAppend = 0x00000004
)

// maxRead is the data size requested per READ; OpenSSH tops out at 256 KiB.
const maxRead = 32 << 10

// sftpStatusError carries a protocol status code.
type sftpStatusError struct {
	Code    uint32
	Message string
	Op      string
}

func (e *sftpStatusError) Error() string {
	msg := e.Message
	if msg == "" {
		switch e.Code {
		case fxNoSuchFile:
			msg = "no such file"
		case fxPermissionDenied:
			msg = "permission denied"
		case fxFailure:
			msg = "failure"
		case fxOpUnsupported:
			msg = "operation not supported"
		default:
			msg = fmt.Sprintf("status %d", e.Code)
		}
	}
	if e.Op != "" {
		return fmt.Sprintf("sftp %s: %s", e.Op, msg)
	}
	return "sftp: " + msg
}

// sftpWire is a request/response client for one SFTP channel.
type sftpWire struct {
	ch      ssh.Channel
	session *ssh.Session
	mu      sync.Mutex
	nextID  uint32
}

func newSFTPWire(client *ssh.Client) (*sftpWire, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	in, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return nil, err
	}
	out, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, err
	}
	if err := session.RequestSubsystem("sftp"); err != nil {
		session.Close()
		return nil, fmt.Errorf("the SSH server refused the sftp subsystem: %w", err)
	}
	wire := &sftpWire{ch: channel{r: out, w: in}, session: session}
	if err := wire.init(); err != nil {
		wire.Close()
		return nil, err
	}
	return wire, nil
}

// channel pairs the session pipes into a single duplex channel.
type channel struct {
	r io.Reader
	w io.Writer
}

func (c channel) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c channel) Write(p []byte) (int, error) { return c.w.Write(p) }
func (c channel) Close() error                { return nil }
func (c channel) CloseWrite() error {
	if cw, ok := c.w.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}
func (c channel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}

type discardReadWriter struct{}

func (discardReadWriter) Read(p []byte) (int, error)  { return 0, io.EOF }
func (discardReadWriter) Write(p []byte) (int, error) { return len(p), nil }

func (c channel) Stderr() io.ReadWriter { return discardReadWriter{} }

func (c *sftpWire) Close() error {
	if c.session != nil {
		return c.session.Close()
	}
	return nil
}

// init performs the version handshake (INIT/VERSION carry no request id).
func (c *sftpWire) init() error {
	p := newPacket()
	p.u8(fxpInit)
	p.u32(3)
	if _, err := c.ch.Write(p.frame()); err != nil {
		return err
	}
	typ, payload, err := c.readPacket()
	if err != nil {
		return err
	}
	if typ != fxpVersion {
		return fmt.Errorf("unexpected SFTP handshake packet %d", typ)
	}
	if len(payload) < 4 {
		return errors.New("truncated SFTP version packet")
	}
	if v := binary.BigEndian.Uint32(payload[:4]); v < 3 {
		return fmt.Errorf("server only speaks SFTP version %d", v)
	}
	return nil
}

// request sends one packet and waits for the matching reply.
func (c *sftpWire) request(typ byte, build func(*packet)) (byte, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	p := newPacket()
	p.u8(typ)
	p.u32(id)
	if build != nil {
		build(p)
	}
	if _, err := c.ch.Write(p.frame()); err != nil {
		return 0, nil, err
	}
	for {
		respType, payload, err := c.readPacket()
		if err != nil {
			return 0, nil, err
		}
		if len(payload) < 4 {
			// VERSION or malformed packet: ignore, keep waiting
			continue
		}
		if binary.BigEndian.Uint32(payload[:4]) != id {
			continue
		}
		return respType, payload[4:], nil
	}
}

// readPacket reads one length prefixed SFTP packet.
func (c *sftpWire) readPacket() (byte, []byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(c.ch, header[:]); err != nil {
		return 0, nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > 1<<20 {
		return 0, nil, fmt.Errorf("invalid SFTP packet size %d", size)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(c.ch, body); err != nil {
		return 0, nil, err
	}
	return body[0], body[1:], nil
}

/* --------------------------- packet (de)serialisation --------------------- */

type packet struct{ b []byte }

func newPacket() *packet { return &packet{} }

func (p *packet) u8(v byte) { p.b = append(p.b, v) }

func (p *packet) u32(v uint32) {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], v)
	p.b = append(p.b, buf[:]...)
}

func (p *packet) u64(v uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	p.b = append(p.b, buf[:]...)
}

func (p *packet) str(s string) {
	p.u32(uint32(len(s)))
	p.b = append(p.b, s...)
}

func (p *packet) blob(b []byte) {
	p.u32(uint32(len(b)))
	p.b = append(p.b, b...)
}

// frame prefixes the payload with its length, as the protocol requires.
func (p *packet) frame() []byte {
	out := make([]byte, 4+len(p.b))
	binary.BigEndian.PutUint32(out[:4], uint32(len(p.b)))
	copy(out[4:], p.b)
	return out
}

type reader struct {
	b   []byte
	off int
	err error
}

func newReader(b []byte) *reader { return &reader{b: b} }

func (r *reader) remaining() int { return len(r.b) - r.off }

func (r *reader) u8() byte {
	if r.err != nil || r.remaining() < 1 {
		r.fail()
		return 0
	}
	v := r.b[r.off]
	r.off++
	return v
}

func (r *reader) u32() uint32 {
	if r.err != nil || r.remaining() < 4 {
		r.fail()
		return 0
	}
	v := binary.BigEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

func (r *reader) u64() uint64 {
	if r.err != nil || r.remaining() < 8 {
		r.fail()
		return 0
	}
	v := binary.BigEndian.Uint64(r.b[r.off:])
	r.off += 8
	return v
}

func (r *reader) str() string {
	n := int(r.u32())
	if r.err != nil || n < 0 || r.remaining() < n {
		r.fail()
		return ""
	}
	v := string(r.b[r.off : r.off+n])
	r.off += n
	return v
}

func (r *reader) blob() []byte {
	n := int(r.u32())
	if r.err != nil || n < 0 || r.remaining() < n {
		r.fail()
		return nil
	}
	v := append([]byte(nil), r.b[r.off:r.off+n]...)
	r.off += n
	return v
}

func (r *reader) fail() {
	if r.err == nil {
		r.err = errors.New("truncated SFTP packet")
	}
}

// attrs reads an attribute structure, returning size, permissions and mtime.
func (r *reader) attrs() (size int64, perms uint32, mtime uint32) {
	flags := r.u32()
	if flags&attrSize != 0 {
		size = int64(r.u64())
	}
	if flags&attrUIDGID != 0 {
		r.u32()
		r.u32()
	}
	if flags&attrPermissions != 0 {
		perms = r.u32()
	}
	if flags&attrACModTime != 0 {
		r.u32() // atime
		mtime = r.u32()
	}
	if flags&attrExtended != 0 {
		count := int(r.u32())
		for i := 0; i < count && r.err == nil; i++ {
			r.str()
			r.str()
		}
	}
	return size, perms, mtime
}

// attrsFor builds the attribute structure used when creating a file.
func attrsFor(mode uint32) func(*packet) {
	return func(p *packet) { p.u32(attrPermissions); p.u32(mode) }
}

// statusError converts a STATUS packet into an error, nil when it is OK.
func statusError(op string, payload []byte) error {
	r := newReader(payload)
	code := r.u32()
	msg := r.str()
	if r.err != nil {
		return fmt.Errorf("sftp %s: malformed status packet", op)
	}
	if code == fxOK {
		return nil
	}
	return &sftpStatusError{Code: code, Message: msg, Op: op}
}

func isSFTPNotExist(err error) bool {
	var se *sftpStatusError
	return errors.As(err, &se) && se.Code == fxNoSuchFile
}

/* ------------------------------- operations ------------------------------- */

func (c *sftpWire) mkdir(path string) error {
	typ, payload, err := c.request(fxpMkdir, func(p *packet) {
		p.str(path)
		p.u32(0) // no attributes
	})
	if err != nil {
		return err
	}
	if typ == fxpStatus {
		err := statusError("mkdir "+path, payload)
		if err != nil && isSFTPNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

func (c *sftpWire) realpath(path string) (string, error) {
	typ, payload, err := c.request(fxpRealpath, func(p *packet) { p.str(path) })
	if err != nil {
		return "", err
	}
	if typ == fxpStatus {
		return "", statusError("realpath "+path, payload)
	}
	r := newReader(payload)
	count := r.u32()
	if count == 0 {
		return "", fmt.Errorf("sftp realpath %s: empty result", path)
	}
	name := r.str() // filename
	r.str()         // longname
	r.attrs()
	if r.err != nil {
		return "", r.err
	}
	return name, nil
}

func (c *sftpWire) open(path string, flags uint32, mode uint32) (string, error) {
	typ, payload, err := c.request(fxpOpen, func(p *packet) {
		p.str(path)
		p.u32(flags)
		p.u32(attrPermissions)
		p.u32(mode)
	})
	if err != nil {
		return "", err
	}
	if typ == fxpStatus {
		return "", statusError("open "+path, payload)
	}
	return string(payload[4:]), nil // handle: length + bytes
}

func (c *sftpWire) close(handle string) error {
	typ, payload, err := c.request(fxpClose, func(p *packet) { p.str(handle) })
	if err != nil {
		return err
	}
	if typ == fxpStatus {
		return statusError("close", payload)
	}
	return nil
}

func (c *sftpWire) write(handle string, offset uint64, data []byte) error {
	typ, payload, err := c.request(fxpWrite, func(p *packet) {
		p.str(handle)
		p.u64(offset)
		p.blob(data)
	})
	if err != nil {
		return err
	}
	if typ == fxpStatus {
		return statusError("write", payload)
	}
	return nil
}

// read returns the next chunk and io.EOF at the end of the file.
func (c *sftpWire) read(handle string, offset uint64, max int) ([]byte, error) {
	typ, payload, err := c.request(fxpRead, func(p *packet) {
		p.str(handle)
		p.u64(offset)
		p.u32(uint32(max))
	})
	if err != nil {
		return nil, err
	}
	switch typ {
	case fxpData:
		r := newReader(payload)
		data := r.blob()
		if r.err != nil {
			return nil, r.err
		}
		return data, nil
	case fxpStatus:
		status := statusError("read", payload)
		var se *sftpStatusError
		if errors.As(status, &se) && se.Code == fxEOF {
			return nil, io.EOF
		}
		return nil, status
	}
	return nil, fmt.Errorf("sftp read: unexpected packet %d", typ)
}

func (c *sftpWire) remove(path string) error {
	typ, payload, err := c.request(fxpRemove, func(p *packet) { p.str(path) })
	if err != nil {
		return err
	}
	if typ == fxpStatus {
		err := statusError("remove "+path, payload)
		if isSFTPNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

func (c *sftpWire) opendir(path string) (string, error) {
	typ, payload, err := c.request(fxpOpendir, func(p *packet) { p.str(path) })
	if err != nil {
		return "", err
	}
	if typ == fxpStatus {
		return "", statusError("opendir "+path, payload)
	}
	return string(payload[4:]), nil
}

type sftpDirEntry struct {
	Name     string
	Size     int64
	Modified uint32
	Dir      bool
}

// readdir returns a batch of directory entries; io.EOF when the directory ends.
func (c *sftpWire) readdir(handle string) ([]sftpDirEntry, error) {
	typ, payload, err := c.request(fxpReaddir, func(p *packet) { p.str(handle) })
	if err != nil {
		return nil, err
	}
	switch typ {
	case fxpName:
		r := newReader(payload)
		count := int(r.u32())
		out := make([]sftpDirEntry, 0, count)
		for i := 0; i < count; i++ {
			name := r.str()
			r.str() // longname
			size, perms, mtime := r.attrs()
			if r.err != nil {
				return nil, r.err
			}
			if name == "." || name == ".." {
				continue
			}
			out = append(out, sftpDirEntry{
				Name:     name,
				Size:     size,
				Modified: mtime,
				Dir:      perms&0o170000 == 0o040000,
			})
		}
		return out, nil
	case fxpStatus:
		status := statusError("readdir", payload)
		var se *sftpStatusError
		if errors.As(status, &se) && se.Code == fxEOF {
			return nil, io.EOF
		}
		return nil, status
	}
	return nil, fmt.Errorf("sftp readdir: unexpected packet %d", typ)
}
