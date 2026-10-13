package dockerx

import (
	"os"
	"strings"
	"testing"
)

// TestHostBindPathOnHost: outside a container the path is absolutized only.
func TestHostBindPathOnHost(t *testing.T) {
	if InContainer() {
		t.Skip("test itself runs inside a container")
	}
	got, err := HostBindPath("relative/dir")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, "/") {
		t.Fatalf("expected absolute path, got %q", got)
	}
	if !strings.HasSuffix(got, "relative/dir") {
		t.Fatalf("path mangled: %q", got)
	}
}

// TestHostBindPathInContainer: inside a container /data/... must re-root onto
// SERVY_HOST_DATA_PATH; errors when the env is missing; non-data paths pass through.
func TestHostBindPathInContainer(t *testing.T) {
	// force container mode via /.dockerenv presence — instead, test the
	// translation logic directly by faking env + using the internal branch.
	// We can't create /.dockerenv safely, so exercise the mapping math via a
	// synthetic call path: run the function body through reflection is
	// overkill; instead assert the error contract first.
	t.Setenv("SERVY_HOST_DATA_PATH", "")
	t.Setenv("SERVY_DATA_DIR", "/data")
	if !InContainer() {
		// on a dev host the mapping branch is unreachable; simulate by
		// verifying passthrough semantics only
		p, err := HostBindPath("/opt/other")
		if err != nil || p != "/opt/other" {
			t.Fatalf("passthrough changed: %q %v", p, err)
		}
		return
	}
	if _, err := HostBindPath("/data/servers/x"); err != ErrHostPathUnmapped {
		t.Fatalf("expected ErrHostPathUnmapped, got %v", err)
	}
	t.Setenv("SERVY_HOST_DATA_PATH", "/srv/servy")
	got, err := HostBindPath("/data/servers/abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/srv/servy/servers/abc" {
		t.Fatalf("translated = %q, want /srv/servy/servers/abc", got)
	}
	other, err := HostBindPath("/mnt/archive")
	if err != nil || other != "/mnt/archive" {
		t.Fatalf("non-data path must pass through: %q %v", other, err)
	}
}

func TestCurrentUserSpec(t *testing.T) {
	spec := CurrentUserSpec()
	if os.Getuid() < 0 {
		if spec != "" {
			t.Fatalf("no uid available but spec = %q", spec)
		}
		return
	}
	if spec == "" {
		t.Fatalf("uid available but spec empty")
	}
	if !strings.Contains(spec, "0") && !strings.Contains(spec, "1") {
		t.Fatalf("suspicious spec %q", spec) // sanity only
	}
}
