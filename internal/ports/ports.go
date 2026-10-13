// Package ports tracks host TCP/UDP port reservations for containerized
// game servers and resolves conflicts before Docker binds them.
//
// Containerized servers publish their own ports on the host, so two servers
// configured for the same port collide at `docker start` with an opaque
// "port is already allocated" error. The Allocator lets the panel detect the
// clash up front, report it to the user, and offer a remapped port.
package ports

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"sync"
)

// Proto is a transport protocol for a reservation ("tcp" or "udp").
type Proto = string

const (
	TCP Proto = "tcp"
	UDP Proto = "udp"
)

var (
	// ErrPortTaken is returned when a port is reserved by another server.
	ErrPortTaken = errors.New("PORT_TAKEN")
	// ErrNoFreePort is returned when no port could be found in the range.
	ErrNoFreePort = errors.New("NO_FREE_PORT")
)

// Reservation is one claimed host port.
type Reservation struct {
	ServerID string `json:"server_id"`
	Port     int    `json:"port"`
	Proto    Proto  `json:"proto"`
}

// Conflict describes a port a server wanted but another server already holds.
type Conflict struct {
	Port       int    `json:"port"`
	Proto      Proto  `json:"proto"`
	HolderID   string `json:"holder_id"`
	HolderName string `json:"holder_name,omitempty"`
}

// key identifies a port+proto pair across all servers.
func key(port int, proto Proto) string {
	return strconv.Itoa(port) + "/" + proto
}

// Allocator tracks per-server host port reservations.
//
// It is safe for concurrent use. Reservations are in-memory and rebuilt from
// the database on panel start, so they reflect intent rather than liveness —
// a crashed container must not silently free its port, otherwise a restart
// could collide with a stale container.
type Allocator struct {
	mu       sync.Mutex
	byKey    map[string]Reservation
	byServer map[string][]string
}

// NewAllocator builds an empty allocator.
func NewAllocator() *Allocator {
	return &Allocator{
		byKey:    map[string]Reservation{},
		byServer: map[string][]string{},
	}
}

// Claim reserves port for serverID. A server may re-claim a port it already
// holds (idempotent, used when settings are patched), but claiming a port held
// by a different server returns ErrPortTaken.
func (a *Allocator) Claim(serverID string, port int, proto Proto) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("invalid port %d", port)
	}
	proto = normalizeProto(proto)
	a.mu.Lock()
	defer a.mu.Unlock()

	k := key(port, proto)
	if existing, ok := a.byKey[k]; ok && existing.ServerID != serverID {
		return fmt.Errorf("%w: %d/%s is held by server %s", ErrPortTaken, port, proto, existing.ServerID)
	}
	if _, ok := a.byKey[k]; !ok {
		a.byServer[serverID] = append(a.byServer[serverID], k)
	}
	a.byKey[k] = Reservation{ServerID: serverID, Port: port, Proto: proto}
	return nil
}

// ClaimAll reserves several ports atomically: on the first conflict nothing
// is reserved, so a partially-claimed server cannot block others.
func (a *Allocator) ClaimAll(serverID string, ports []Reservation) error {
	// Validate everything first so we stay atomic.
	seen := map[string]bool{}
	type pending struct {
		port int
		proto Proto
	}
	var todo []pending
	for _, p := range ports {
		proto := normalizeProto(p.Proto)
		k := key(p.Port, proto)
		if seen[k] {
			continue // same port twice in one request
		}
		seen[k] = true
		a.mu.Lock()
		existing, ok := a.byKey[k]
		a.mu.Unlock()
		if ok && existing.ServerID != serverID {
			return fmt.Errorf("%w: %d/%s is held by server %s", ErrPortTaken, p.Port, proto, existing.ServerID)
		}
		todo = append(todo, pending{p.Port, proto})
	}
	for _, p := range todo {
		if err := a.Claim(serverID, p.port, p.proto); err != nil {
			return err
		}
	}
	return nil
}

// Release frees every port held by serverID. Call before removing a server so
// its ports become reusable.
func (a *Allocator) Release(serverID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, k := range a.byServer[serverID] {
		delete(a.byKey, k)
	}
	delete(a.byServer, serverID)
}

// Held returns the ports currently reserved by serverID.
func (a *Allocator) Held(serverID string) []Reservation {
	a.mu.Lock()
	defer a.mu.Unlock()
	keys := a.byServer[serverID]
	out := make([]Reservation, 0, len(keys))
	for _, k := range keys {
		if r, ok := a.byKey[k]; ok {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port == out[j].Port {
			return out[i].Proto < out[j].Proto
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// Conflicts returns ports in want that are reserved by a *different* server.
// This is what the panel surfaces to the user when a save is loaded that
// collides with an existing server on this host.
func (a *Allocator) Conflicts(serverID string, want []Reservation) []Conflict {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Conflict
	for _, p := range want {
		proto := normalizeProto(p.Proto)
		if r, ok := a.byKey[key(p.Port, proto)]; ok && r.ServerID != serverID {
			out = append(out, Conflict{Port: p.Port, Proto: proto, HolderID: r.ServerID})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port == out[j].Port {
			return out[i].Proto < out[j].Proto
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// Free reports whether port can be bound right now by *any* process on this
// host. Reservation tracking catches servy-vs-servy clashes; this catches
// clashes with unrelated processes already holding the port.
//
// Docker publishes on all interfaces by default, so the all-interfaces bind is
// the authoritative check. We additionally probe loopback because Windows (and
// Linux without SO_REUSEADDR set on the holder) permits a wildcard bind while a
// process still holds the same port on a specific address — probing only the
// wildcard would then report a busy port as free.
func Free(port int, proto Proto) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	if normalizeProto(proto) == UDP {
		return udpFree(port)
	}
	return tcpFree(port)
}

func tcpFree(port int) bool {
	for _, host := range []string{"", "127.0.0.1", "::1"} {
		l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			return false
		}
		_ = l.Close()
	}
	return true
}

func udpFree(port int) bool {
	for _, host := range []string{"", "127.0.0.1", "::1"} {
		c, err := net.ListenPacket("udp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			return false
		}
		_ = c.Close()
	}
	return true
}

// Available combines both checks: not reserved by another servy server AND
// bindable on the host right now.
func (a *Allocator) Available(serverID string, port int, proto Proto) bool {
	proto = normalizeProto(proto)
	a.mu.Lock()
	if r, ok := a.byKey[key(port, proto)]; ok && r.ServerID != serverID {
		a.mu.Unlock()
		return false
	}
	a.mu.Unlock()
	return Free(port, proto)
}

// FindFree returns preferred when it is usable, otherwise scans [lo, hi] for
// the first usable port. It never returns a port reserved by another server.
func (a *Allocator) FindFree(serverID string, preferred, lo, hi int, proto Proto) (int, error) {
	if preferred > 0 && a.Available(serverID, preferred, proto) {
		return preferred, nil
	}
	if hi <= 0 {
		hi = lo
	}
	for p := lo; p <= hi; p++ {
		if p <= 0 || p > 65535 {
			continue
		}
		if a.Available(serverID, p, proto) {
			return p, nil
		}
	}
	return 0, fmt.Errorf("%w: no free %s port in [%d-%d] for server %s", ErrNoFreePort, proto, lo, hi, serverID)
}

func normalizeProto(p Proto) Proto {
	if p == UDP {
		return UDP
	}
	return TCP
}