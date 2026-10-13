package dockerx

import (
	"testing"
	"time"
)

func TestBuildPortMap(t *testing.T) {
	set, bindings, err := buildPortMap([]string{"25565:25565/tcp", "19132:19132/udp", "27015/tcp"})
	if err != nil {
		t.Fatalf("buildPortMap: %v", err)
	}
	if _, ok := set["25565/tcp"]; !ok {
		t.Error("25565/tcp not exposed")
	}
	if _, ok := set["19132/udp"]; !ok {
		t.Error("19132/udp not exposed")
	}
	if _, ok := set["27015/tcp"]; !ok {
		t.Error("27015/tcp not exposed (implicit host port)")
	}
	if b := bindings["25565/tcp"]; len(b) != 1 || b[0].HostPort != "25565" {
		t.Errorf("25565 binding wrong: %+v", b)
	}
	if b := bindings["19132/udp"]; len(b) != 1 || b[0].HostPort != "19132" {
		t.Errorf("19132 binding wrong: %+v", b)
	}
	// empty + whitespace tolerated
	if _, _, err := buildPortMap([]string{"", "  "}); err != nil {
		t.Errorf("empty specs should not error: %v", err)
	}
	// invalid spec errors
	if _, _, err := buildPortMap([]string{"not-a-port"}); err == nil {
		t.Error("invalid port spec must error")
	}
}

func TestStatsPercent(t *testing.T) {
	prev := &ContainerStats{CPUTotal: 1_000_000_000, SystemTotal: 10_000_000_000, OnlineCPUs: 4}
	cur := &ContainerStats{CPUTotal: 2_000_000_000, SystemTotal: 20_000_000_000, OnlineCPUs: 4}
	// cpu/system = 1/10 of 4 cores = 40%
	if got := StatsPercent(prev, cur, int64(5*time.Second)); got < 39.9 || got > 40.1 {
		t.Errorf("StatsPercent = %v, want ~40", got)
	}
	if got := StatsPercent(nil, cur, int64(time.Second)); got != 0 {
		t.Errorf("nil prev must be 0, got %v", got)
	}
	if got := StatsPercent(prev, cur, 0); got != 0 {
		t.Errorf("zero delta must be 0, got %v", got)
	}
}

func TestNilManagerSafety(t *testing.T) {
	var m *Manager
	if m.Available() {
		t.Error("nil manager must report unavailable")
	}
	if m.Host() != "" {
		t.Error("nil manager host should be empty")
	}
	if _, err := m.Stats(nil, "x"); err != ErrUnavailable {
		t.Errorf("nil manager Stats: %v", err)
	}
	if err := m.Kill(nil, "x"); err != ErrUnavailable {
		t.Errorf("nil manager Kill: %v", err)
	}
}
