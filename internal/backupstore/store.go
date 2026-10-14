// Package backupstore implements the remote storage destinations a backup can
// be uploaded to: S3-compatible object storage (AWS, MinIO, Cloudflare R2,
// Backblaze B2, DigitalOcean Spaces, Wasabi, …), FTP/FTPS, SFTP and WebDAV,
// plus plain local directories.
//
// Every destination lives in its own namespace. Object keys are always built as
//
//	<prefix>/<cluster-id>/<server-id>/<archive-name>
//
// so two panels (clusters) sharing one bucket or FTP account can never
// overwrite each other's archives, and a node re-pointed at a different panel
// keeps writing into that panel's namespace.
package backupstore

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// ErrNotExist is returned by Get/List when an object is not there. Callers use
// it to tell "not uploaded yet" from a real failure.
var ErrNotExist = errors.New("object not found")

// newHTTPClient builds the client used by the HTTP based backends. timeout of 0
// means "no total timeout" (large archives), individual phases stay bounded.
func newHTTPClient(insecure bool, timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   30 * time.Second,
		ExpectContinueTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 3 * time.Minute,
		ForceAttemptHTTP2:     true,
	}
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 - explicit user choice
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

// Kinds supported by the factory. They are stored verbatim in the database and
// used by the panel UI to pick the right form.
const (
	KindLocal  = "local"
	KindS3     = "s3"
	KindFTP    = "ftp"
	KindSFTP   = "sftp"
	KindWebDAV = "webdav"
)

// Kinds lists every supported destination kind in menu order.
var Kinds = []string{KindLocal, KindS3, KindFTP, KindSFTP, KindWebDAV}

// Object is a single entry stored in a destination.
type Object struct {
	Key      string    `json:"key"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// Store is a minimal, append/read/delete/list abstraction over a destination.
//
// Keys passed to a Store are relative to the destination prefix: a store knows
// its own root (bucket, login directory, collection URL) and the caller builds
// the rest with Key(). Implementations must accept the "/"-separated key of
// Key() and create missing directories/collections on demand.
type Store interface {
	// Put writes an object of the given size. A negative size means the size is
	// unknown and the payload has to be buffered/spooled by the backend.
	Put(ctx context.Context, key string, body io.Reader, size int64) error
	// Get opens an object for reading. The caller closes it.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete removes an object. Removing something that is not there is not an
	// error.
	Delete(ctx context.Context, key string) error
	// List returns every object below prefix, sorted by key.
	List(ctx context.Context, prefix string) ([]Object, error)
	// Ping verifies connectivity and credentials.
	Ping(ctx context.Context) error
	// Kind reports the backend kind.
	Kind() string
	// Location is a human readable description of where objects land, without
	// the namespace part (e.g. "s3://bucket/prefix", "ftp://host/dir").
	Location() string
}

// Destination is the persisted configuration of a storage destination.
type Destination struct {
	ID        string            `json:"storage_id"`
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	Prefix    string            `json:"prefix"`
	Settings  map[string]string `json:"settings"`
	Enabled   bool              `json:"enabled"`
	Default   bool              `json:"default"`
	ClusterID string            `json:"cluster_id"`
}

// Namespace is the per-cluster key prefix every object of this destination is
// written below. Two panels sharing one bucket or FTP account never overlap
// because their namespace differs.
func (d Destination) Namespace() string { return ClusterPrefix(d.ClusterID) }

// DisplayLocation renders where objects land without opening the destination,
// so the panel can show it in the list of storages.
func (d Destination) DisplayLocation() string {
	prefix := cleanPrefix(d.Prefix)
	join := func(base string) string {
		if prefix == "" {
			return base
		}
		return strings.TrimSuffix(base, "/") + "/" + prefix
	}
	host := d.Settings["host"]
	port := d.Settings["port"]
	user := d.Settings["username"]
	at := ""
	if user != "" {
		at = user + "@"
	}
	if port != "" {
		host = host + ":" + port
	}
	switch d.Kind {
	case KindLocal:
		return join(d.Settings["path"])
	case KindS3:
		return join("s3://" + d.Settings["bucket"])
	case KindFTP:
		return join("ftp://" + at + host)
	case KindSFTP:
		return join("sftp://" + at + host)
	case KindWebDAV:
		return join(strings.TrimSuffix(d.Settings["url"], "/"))
	}
	return prefix
}

// KnownSetting reports whether a key belongs to the settings of a kind.
func KnownSetting(kind, key string) bool {
	for _, s := range Settings(kind) {
		if s.Key == key {
			return true
		}
	}
	return false
}

// Setting describes one input of a destination kind. The panel renders the form
// from this list, so a new backend only has to declare its fields here.
type Setting struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Hint     string   `json:"hint,omitempty"`
	Type     string   `json:"type"` // text | password | number | boolean | select
	Required bool     `json:"required"`
	Default  string   `json:"default,omitempty"`
	Options  []Option `json:"options,omitempty"`
}

// Option is one choice of a select setting.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Settings returns the settings a given kind understands, in form order.
// The panel renders the form from this list, so adding a backend means adding
// one case here plus the implementation.
func Settings(kind string) []Setting {
	switch kind {
	case KindLocal:
		return []Setting{
			{Key: "path", Label: "Directory", Hint: "Absolute path on the machine that runs the backup. It is created if missing.", Type: "text", Required: true},
		}
	case KindS3:
		return []Setting{
			{Key: "bucket", Label: "Bucket", Type: "text", Required: true},
			{Key: "endpoint", Label: "Endpoint", Hint: "Leave empty for AWS. Set it for MinIO, R2, Spaces, Wasabi, … e.g. https://minio.example.com:9000", Type: "text"},
			{Key: "region", Label: "Region", Type: "text", Default: "us-east-1"},
			{Key: "access_key", Label: "Access key", Type: "text", Required: true},
			{Key: "secret_key", Label: "Secret key", Type: "password", Required: true},
			{Key: "session_token", Label: "Session token", Hint: "Only for temporary STS credentials.", Type: "password"},
			{Key: "path_style", Label: "Path style requests", Hint: "Required by MinIO and most self-hosted gateways.", Type: "boolean", Default: "true"},
			{Key: "insecure", Label: "Skip TLS verification", Type: "boolean"},
		}
	case KindFTP:
		return []Setting{
			{Key: "host", Label: "Host", Type: "text", Required: true},
			{Key: "port", Label: "Port", Type: "number", Default: "21"},
			{Key: "username", Label: "Username", Type: "text", Required: true},
			{Key: "password", Label: "Password", Type: "password"},
			{Key: "tls", Label: "Encryption", Type: "select", Default: "none", Options: []Option{
				{Value: "none", Label: "None (plain FTP)"},
				{Value: "explicit", Label: "FTPS (AUTH TLS)"},
				{Value: "implicit", Label: "FTPS (implicit, port 990)"},
			}},
			{Key: "insecure", Label: "Skip TLS verification", Type: "boolean"},
		}
	case KindSFTP:
		return []Setting{
			{Key: "host", Label: "Host", Type: "text", Required: true},
			{Key: "port", Label: "Port", Type: "number", Default: "22"},
			{Key: "username", Label: "Username", Type: "text", Required: true},
			{Key: "password", Label: "Password", Type: "password"},
			{Key: "private_key", Label: "Private key", Hint: "PEM encoded key. Takes precedence over the password.", Type: "text"},
			{Key: "passphrase", Label: "Key passphrase", Type: "password"},
			{Key: "host_key", Label: "Pinned host key", Hint: "Optional. Without it the first connection is trusted and the key is remembered on this machine.", Type: "text"},
		}
	case KindWebDAV:
		return []Setting{
			{Key: "url", Label: "Collection URL", Hint: "e.g. https://cloud.example.com/remote.php/dav/files/user/backups", Type: "text", Required: true},
			{Key: "username", Label: "Username", Type: "text"},
			{Key: "password", Label: "Password", Type: "password"},
			{Key: "insecure", Label: "Skip TLS verification", Type: "boolean"},
		}
	}
	return nil
}

// IsSecret reports whether a setting must never be sent back to the browser.
func IsSecret(key string) bool {
	switch key {
	case "secret_key", "session_token", "password", "passphrase", "private_key":
		return true
	}
	return false
}

// ValidKind reports whether kind is one of the supported backends.
func ValidKind(kind string) bool {
	for _, k := range Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// Validate checks a destination configuration and returns a user facing error.
// Unknown keys are rejected so typos do not silently disappear.
func Validate(kind string, settings map[string]string) error {
	if !ValidKind(kind) {
		return fmt.Errorf("unknown storage type %q", kind)
	}
	known := map[string]bool{}
	for _, s := range Settings(kind) {
		known[s.Key] = true
	}
	for k := range settings {
		if !known[k] {
			return fmt.Errorf("unknown setting %q for %s storage", k, kind)
		}
	}
	for _, s := range Settings(kind) {
		if s.Required && strings.TrimSpace(settings[s.Key]) == "" {
			return fmt.Errorf("%s is required", strings.ToLower(s.Label))
		}
	}
	switch kind {
	case KindFTP:
		if v := settings["tls"]; v != "" && v != "none" && v != "explicit" && v != "implicit" {
			return fmt.Errorf("unknown encryption mode %q", v)
		}
	case KindWebDAV:
		if u := strings.TrimSpace(settings["url"]); u != "" {
			if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
				return fmt.Errorf("collection URL must start with http:// or https://")
			}
		}
	case KindS3:
		if e := strings.TrimSpace(settings["endpoint"]); e != "" {
			if !strings.HasPrefix(e, "http://") && !strings.HasPrefix(e, "https://") {
				return fmt.Errorf("endpoint must start with http:// or https://")
			}
		}
	}
	switch kind {
	case KindFTP, KindSFTP:
		if v := strings.TrimSpace(settings["port"]); v != "" {
			port, err := strconv.Atoi(v)
			if err != nil || port <= 0 || port > 65535 {
				return fmt.Errorf("invalid port %q", v)
			}
		}
	}
	if strings.ContainsAny(settings["path"], "\x00") {
		return fmt.Errorf("invalid path")
	}
	return nil
}

// Open builds a Store for a destination. clusterID is stored alongside the
// configuration by the caller; it does not take part in building the store.
func Open(dest Destination) (Store, error) {
	if err := Validate(dest.Kind, dest.Settings); err != nil {
		return nil, err
	}
	prefix := cleanPrefix(dest.Prefix)
	switch dest.Kind {
	case KindLocal:
		if strings.TrimSpace(dest.Settings["path"]) == "" {
			return nil, fmt.Errorf("directory is required")
		}
		return newLocalStore(dest.Settings["path"], prefix), nil
	case KindS3:
		return newS3Store(dest.Settings, prefix)
	case KindFTP:
		return newFTPStore(dest.Settings, prefix)
	case KindSFTP:
		return newSFTPStore(dest.Settings, prefix)
	case KindWebDAV:
		return newWebDAVStore(dest.Settings, prefix)
	}
	return nil, fmt.Errorf("unknown storage type %q", dest.Kind)
}

// SanitizeSegment makes one path segment of an object key safe. Slashes stay as
// separators, everything unusual becomes "_", and "."/".." segments become "_"
// as well so a name can never climb out of, or collapse into, another
// namespace (path.Join would silently resolve "..").
func SanitizeSegment(s string) string {
	s = strings.ReplaceAll(s, `\`, "/")
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '/':
			return r
		default:
			return '_'
		}
	}, s)
	parts := strings.Split(out, "/")
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(part, ".")
		if part == "" {
			part = "_"
		}
		if len(part) > 128 {
			part = part[:128]
		}
		cleaned = append(cleaned, part)
	}
	res := strings.Join(cleaned, "/")
	if res == "" {
		return "_"
	}
	return res
}

// Key builds the namespaced object key for an archive:
//
//	<cluster>/<server>/<archive>
//
// The cluster id keeps separate Servy clusters from overwriting each other in a
// shared destination, the server level keeps the archives of one server
// together. It matches the layout the archives have on local disk.
func Key(clusterID, serverID, name string) string {
	return path.Join(SanitizeSegment(clusterID), SanitizeSegment(serverID), SanitizeSegment(name))
}

// PrefixFor returns the key prefix of one server inside a cluster.
func PrefixFor(clusterID, serverID string) string {
	return path.Join(SanitizeSegment(clusterID), SanitizeSegment(serverID))
}

// ClusterPrefix returns the key prefix of a whole cluster.
func ClusterPrefix(clusterID string) string {
	return SanitizeSegment(clusterID)
}

// cleanPrefix normalises a user supplied prefix.
func cleanPrefix(prefix string) string {
	parts := []string{}
	for _, p := range strings.FieldsFunc(strings.ReplaceAll(prefix, `\`, "/"), func(r rune) bool { return r == '/' }) {
		if p == "" || p == "." || p == ".." {
			continue
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "/")
}

// splitKey returns the individual segments of a relative key.
func splitKey(key string) []string {
	out := []string{}
	for _, p := range strings.Split(strings.ReplaceAll(key, `\`, "/"), "/") {
		if p == "" || p == "." || p == ".." {
			continue
		}
		out = append(out, p)
	}
	return out
}

// relKey converts a full path inside a destination (prefix included) into a key
// relative to the destination prefix, which is the key space callers use.
func relKey(full, prefix string) (string, bool) {
	full = strings.TrimPrefix(strings.ReplaceAll(full, `\\`, "/"), "/")
	prefix = cleanPrefix(prefix)
	if prefix == "" {
		return full, full != ""
	}
	if full == prefix {
		return "", false
	}
	if !strings.HasPrefix(full, prefix+"/") {
		return "", false
	}
	rel := strings.TrimPrefix(full, prefix+"/")
	return rel, rel != ""
}

// LocalDir returns the directory a local destination writes to, prefix
// included. Used by the API to show where archives end up.
func (d Destination) LocalDir() string {
	if d.Kind != KindLocal {
		return ""
	}
	return path.Join(d.Settings["path"], cleanPrefix(d.Prefix))
}
