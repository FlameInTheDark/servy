package ports

import (
	"errors"
	"net"
	"strconv"
	"testing"
)

// freePortPair reserves two consecutive host ports so the test never races a
// real service: bind :0 to let the OS pick, then read back what we got.
func freePorts(t *testing.T, proto Proto, n int) []int {
	t.Helper()
	var out []int
	for i := 0; i < n; i++ {
		var port int
		switch proto {
		case UDP:
			c, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen udp: %v", err)
			}
			port = c.LocalAddr().(*net.UDPAddr).Port
			_ = c.Close()
		default:
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen tcp: %v", err)
			}
			port = l.Addr().(*net.TCPAddr).Port
			_ = l.Close()
		}
		out = append(out, port)
	}
	return out
}

func TestClaimIsExclusive(t *testing.T) {
	a := NewAllocator()
	p := freePorts(t, TCP, 1)[0]

	if err := a.Claim("srv-a", p, TCP); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// Same server re-claiming is idempotent (settings patch flow).
	if err := a.Claim("srv-a", p, TCP); err != nil {
		t.Fatalf("idempotent re-claim: %v", err)
	}
	// A different server must be rejected.
	err := a.Claim("srv-b", p, TCP)
	if !errors.Is(err, ErrPortTaken) {
		t.Fatalf("second server claim err = %v, want ErrPortTaken", err)
	}
}

func TestClaimProtoIsIndependent(t *testing.T) {
	a := NewAllocator()
	p := freePorts(t, TCP, 1)[0]

	if err := a.Claim("srv-a", p, TCP); err != nil {
		t.Fatalf("claim tcp: %v", err)
	}
	// Same port over UDP is a different reservation and must be allowed.
	if err := a.Claim("srv-b", p, UDP); err != nil {
		t.Fatalf("claim udp on same number: %v", err)
	}
}

func TestReleaseFreesPort(t *testing.T) {
	a := NewAllocator()
	p := freePorts(t, TCP, 1)[0]

	_ = a.Claim("srv-a", p, TCP)
	_ = a.Claim("srv-b", p, TCP) // must fail
	a.Release("srv-a")

	if err := a.Claim("srv-b", p, TCP); err != nil {
		t.Fatalf("claim after release: %v", err)
	}
	if got := len(a.Held("srv-a")); got != 0 {
		t.Fatalf("srv-a holds %d ports after release, want 0", got)
	}
}

func TestClaimAllIsAtomic(t *testing.T) {
	a := NewAllocator()
	busy := freePorts(t, TCP, 1)[0]
	spare := freePorts(t, TCP, 1)[0]

	_ = a.Claim("srv-b", busy, TCP)

	// Claim two for srv-a where the second one collides: nothing should stick.
	err := a.ClaimAll("srv-a", []Reservation{
		{Port: spare, Proto: TCP},
		{Port: busy, Proto: TCP},
	})
	if !errors.Is(err, ErrPortTaken) {
		t.Fatalf("err = %v, want ErrPortTaken", err)
	}
	if got := len(a.Held("srv-a")); got != 0 {
		t.Fatalf("srv-a holds %d ports after failed ClaimAll, want 0 (atomic)", got)
	}
	// The pre-existing holder is untouched.
	if got := len(a.Held("srv-b")); got != 1 {
		t.Fatalf("srv-b holds %d ports, want 1", got)
	}
}

func TestConflictsReportsHolder(t *testing.T) {
	a := NewAllocator()
	p := freePorts(t, TCP, 1)[0]

	_ = a.Claim("srv-a", p, TCP)

	got := a.Conflicts("srv-b", []Reservation{{Port: p, Proto: TCP}})
	if len(got) != 1 {
		t.Fatalf("conflicts = %d, want 1", len(got))
	}
	if got[0].HolderID != "srv-a" {
		t.Fatalf("holder = %q, want srv-a", got[0].HolderID)
	}
	// A server never conflicts with itself.
	if c := a.Conflicts("srv-a", []Reservation{{Port: p, Proto: TCP}}); len(c) != 0 {
		t.Fatalf("self-conflict = %d, want 0", len(c))
	}
}

func TestFindFreeSkipsReserved(t *testing.T) {
	a := NewAllocator()
	lo := 40000
	hi := 40020
	taken := freePorts(t, TCP, 1)[0]
	_ = a.Claim("srv-a", taken, TCP)

	// Pretend the OS-level check passes by asking for a range and skipping
	// the reserved entry: FindFree must not return the reserved port.
	got, err := a.FindFree("srv-b", taken, lo, hi, TCP)
	if err != nil {
		t.Fatalf("FindFree: %v", err)
	}
	if got == taken {
		t.Fatalf("FindFree returned the reserved port %d", taken)
	}
}

func TestFindFreeReturnsPreferredWhenAvailable(t *testing.T) {
	a := NewAllocator()
	p := freePorts(t, TCP, 1)[0]
	got, err := a.FindFree("srv-a", p, 40000, 40100, TCP)
	if err != nil {
		t.Fatalf("FindFree: %v", err)
	}
	if got != p {
		t.Fatalf("FindFree = %d, want preferred %d", got, p)
	}
}

func TestFreeDetectsHeldPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	if Free(port, TCP) {
		t.Fatalf("Free(%d, tcp) = true while the port is actively bound", port)
	}
	// An unreserved-but-unbound port in the same range must be reported free.
	if !Free(port+1, TCP) {
		// port+1 may legitimately be taken; only fail if it is unbound too.
		if c, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port+1)); err == nil {
			c.Close()
			t.Fatalf("Free(%d, tcp) = false although the port is free", port+1)
		}
	}
}