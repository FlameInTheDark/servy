package cluster

import (
	"testing"
	"time"

	pb "github.com/FlameInTheDark/servy/internal/cluster/pb"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
)

func TestTicketLifecycle(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	store := NewTicketStore()
	store.now = func() time.Time { return now }

	secret, expires := store.Issue("build box")
	if secret == "" {
		t.Fatal("issue returned an empty ticket")
	}
	if !expires.After(now) {
		t.Fatal("ticket expiry must be in the future")
	}
	if len(store.Pending()) != 1 {
		t.Fatalf("pending = %d, want 1", len(store.Pending()))
	}

	// redeem consumes it
	if note, ok := store.Redeem(secret, "node-1"); !ok || note != "build box" {
		t.Fatalf("redeem = (%q, %v), want (build box, true)", note, ok)
	}
	if _, ok := store.Redeem(secret, "node-2"); ok {
		t.Fatal("a ticket must be single use")
	}
	if len(store.Pending()) != 0 {
		t.Fatal("redeemed tickets must disappear from the pending list")
	}

	// expiry
	old, _ := store.Issue("stale")
	now = now.Add(TicketTTL + time.Minute)
	if _, ok := store.Redeem(old, "node-3"); ok {
		t.Fatal("an expired ticket must not be redeemable")
	}

	// revoke
	revoked, _ := store.Issue("revoke me")
	store.Revoke(hashToken(revoked))
	if _, ok := store.Redeem(revoked, "node-4"); ok {
		t.Fatal("a revoked ticket must not be redeemable")
	}
	if _, ok := store.Redeem("", "node-5"); ok {
		t.Fatal("an empty ticket must never be accepted")
	}
}

func TestNodeRegistryLifecycle(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	reg := NewNodeRegistry()
	reg.now = func() time.Time { return now }

	st := reg.Register("node-1", "node one", "10.0.0.5:50052", "1.0", "secret-token", []string{"servers"})
	if !st.Online() {
		t.Fatal("a freshly registered node must be online")
	}
	if id, ok := reg.Authenticate("secret-token"); !ok || id != "node-1" {
		t.Fatalf("authenticate = (%q, %v)", id, ok)
	}
	if reg.Token("node-1") != "secret-token" {
		t.Fatal("the panel must keep the credential used to dial the node")
	}
	if _, ok := reg.Authenticate("wrong"); ok {
		t.Fatal("unknown credentials must not authenticate")
	}

	reg.ApplyHeartbeat("node-1", &pb.HeartbeatRequest{CpuUsage: 10, MemPercent: 20, TotalServers: 3, RunningServers: 2})
	got, _ := reg.Get("node-1")
	if got.CPU != 10 || got.MemPct != 20 || got.Total != 3 || got.Running != 2 {
		t.Fatalf("heartbeat telemetry not stored: %+v", got)
	}

	reg.ApplyServers("node-1", []*pb.ServerStatusResponse{{ServerId: "s1", State: "running"}})
	if status, nodeID, ok := reg.ServerStatus("s1"); !ok || nodeID != "node-1" || status.State != "running" {
		t.Fatalf("server status cache = (%+v, %q, %v)", status, nodeID, ok)
	}
	reg.UpdateServerStatus("node-1", "s1", "stopped")
	if status, _, _ := reg.ServerStatus("s1"); status.State != "stopped" {
		t.Fatalf("event-driven status update lost: %+v", status)
	}

	// sweeper marks silence as offline
	now = now.Add(OfflineAfter + time.Second)
	if gone := reg.Sweep(); len(gone) != 1 || gone[0] != "node-1" {
		t.Fatalf("sweep = %v, want [node-1]", gone)
	}
	if st, _ := reg.Get("node-1"); st.Online() {
		t.Fatal("a silent node must go offline")
	}

	// forgetting drops the credential with the node
	reg.Forget("node-1")
	if _, ok := reg.Authenticate("secret-token"); ok {
		t.Fatal("a forgotten node's credential must stop working")
	}
	if _, ok := reg.Get("node-1"); ok {
		t.Fatal("a forgotten node must disappear from the registry")
	}
}

func TestNodeRegistryReloadFromRows(t *testing.T) {
	reg := NewNodeRegistry()
	addr := "10.1.2.3:50052"
	version := "2.0"
	lastSeen := "2026-10-14T11:00:00Z"
	token := "persisted-token"
	reg.Load([]gen.Node{{
		NodeID: "node-9", Name: "restored", Address: addr, Role: "agent", Status: "offline",
		Version: &version, LastSeen: &lastSeen, ClusterTokenHash: &token, Created: lastSeen,
	}})
	st, ok := reg.Get("node-9")
	if !ok || st.Address != addr || st.Version != version {
		t.Fatalf("node row not restored: %+v (%v)", st, ok)
	}
	if id, ok := reg.Authenticate("persisted-token"); !ok || id != "node-9" {
		t.Fatal("a restored node must authenticate with its stored credential so the panel can dial it after a restart")
	}
}

func TestDeriveNodeAddress(t *testing.T) {
	cases := []struct {
		advertise, peer string
		port            int32
		want            string
	}{
		{"", "10.0.0.9", 50052, "10.0.0.9:50052"},
		{"node.example.com:6000", "10.0.0.9", 50052, "node.example.com:6000"},
		{"", "", 0, "127.0.0.1:50052"},
	}
	for _, c := range cases {
		if got := deriveNodeAddress(c.advertise, c.peer, c.port); got != c.want {
			t.Errorf("deriveNodeAddress(%q, %q, %d) = %q, want %q", c.advertise, c.peer, c.port, got, c.want)
		}
	}
}

func TestSanitizeNodeName(t *testing.T) {
	if got := sanitizeNodeName("build box #1"); got != "build-box-1" {
		t.Errorf("sanitizeNodeName = %q", got)
	}
	long := make([]byte, 80)
	for i := range long {
		long[i] = 'a'
	}
	if len(sanitizeNodeName(string(long))) > 48 {
		t.Error("node names must be length capped")
	}
}
