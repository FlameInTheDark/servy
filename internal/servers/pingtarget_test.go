package servers

import (
	"testing"

	"github.com/FlameInTheDark/servy/internal/repo/gen"
)

// Panel-created servers pin def.ServerIp to 127.0.0.1 — that only reaches a
// docker-backend server when servy itself runs on the host. pingTarget must
// pass the stored address through untouched whenever the container IP cannot
// be resolved (no docker manager, no running container), and the stats
// collector falls back to it instead of failing.
func TestPingTargetFallbacks(t *testing.T) {
	// process backend: stored address passes through
	s := &Supervisor{def: gen.Server{ServerIp: "10.0.0.5", ExecutionBackend: "process"}}
	if got := s.pingTarget(); got != "10.0.0.5" {
		t.Fatalf("process backend target = %q, want 10.0.0.5", got)
	}

	// docker backend, docker manager unavailable: stored address
	s = &Supervisor{def: gen.Server{ServerIp: "127.0.0.1", ExecutionBackend: "docker"}}
	if got := s.pingTarget(); got != "127.0.0.1" {
		t.Fatalf("docker backend without manager target = %q, want 127.0.0.1", got)
	}

	// docker backend with an empty ServerIp def (older records): still empty
	// here — PingServer normalizes empty to 127.0.0.1
	s = &Supervisor{def: gen.Server{ExecutionBackend: "docker"}}
	if got := s.pingTarget(); got != "" {
		t.Fatalf("docker backend empty-ip target = %q, want empty", got)
	}
}
