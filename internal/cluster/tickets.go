package cluster

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// TicketTTL is how long a join ticket stays valid.
const TicketTTL = 10 * time.Minute

// Ticket is a single-use join credential issued by the panel. Only its hash is
// kept in memory, so a leaked dump of the ticket store is not enough to join.
type Ticket struct {
	Hash    string
	Note    string
	Created time.Time
	Expires time.Time
	UsedBy  string
}

// TicketStore holds pending join tickets. Tickets are intentionally in-memory:
// they are short lived, single use, and invalidating them on restart is the
// safe default.
type TicketStore struct {
	mu      sync.Mutex
	tickets map[string]*Ticket
	now     func() time.Time
}

// NewTicketStore builds an empty store.
func NewTicketStore() *TicketStore {
	return &TicketStore{tickets: map[string]*Ticket{}, now: time.Now}
}

// Issue creates a ticket and returns its plaintext form once.
func (s *TicketStore) Issue(note string) (string, time.Time) {
	secret := randomHex(24)
	t := &Ticket{
		Hash:    hashToken(secret),
		Note:    note,
		Created: s.now(),
		Expires: s.now().Add(TicketTTL),
	}
	s.mu.Lock()
	s.gcLocked()
	s.tickets[t.Hash] = t
	s.mu.Unlock()
	return secret, t.Expires
}

// Redeem consumes a ticket. It returns the note and true on success.
func (s *TicketStore) Redeem(secret, nodeID string) (string, bool) {
	if secret == "" {
		return "", false
	}
	h := hashToken(secret)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	t, ok := s.tickets[h]
	if !ok || t.UsedBy != "" || s.now().After(t.Expires) {
		return "", false
	}
	t.UsedBy = nodeID
	return t.Note, true
}

// Pending lists tickets that have not been used yet (for the UI).
func (s *TicketStore) Pending() []Ticket {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	out := []Ticket{}
	for _, t := range s.tickets {
		if t.UsedBy == "" {
			out = append(out, *t)
		}
	}
	return out
}

// Revoke drops a pending ticket by hash.
func (s *TicketStore) Revoke(hash string) {
	s.mu.Lock()
	delete(s.tickets, hash)
	s.mu.Unlock()
}

func (s *TicketStore) gcLocked() {
	now := s.now()
	for h, t := range s.tickets {
		if t.UsedBy != "" || now.After(t.Expires) {
			delete(s.tickets, h)
		}
	}
}

// randomHex returns n random bytes hex encoded (2n characters).
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand never fails in practice; fall back to a time seed rather
		// than emitting an empty credential.
		seed := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		copy(b, seed[:])
	}
	return hex.EncodeToString(b)
}

func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(secret)))
	return hex.EncodeToString(sum[:])
}

// ConstantTimeEqual compares two tokens without leaking timing.
func ConstantTimeEqual(a, b string) bool {
	if a == "" || b == "" || len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// nodeID generates a stable-looking node identifier.
func newNodeID() string {
	return "node-" + randomHex(6)
}
