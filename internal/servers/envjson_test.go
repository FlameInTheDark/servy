package servers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The docker backend must read env.json from the server dir on the panel host
// (def.Path) while resolving relative path entries against the in-container
// workdir. Resolving against the panel path would leak a panel-side absolute
// path into the container; reading from the workdir would miss the file
// entirely — either way the game never sees LD_LIBRARY_PATH and dies with
// "GameServer.Init() failed" (observed with valheim in sibling containers).
func TestApplyEnvJSONResolveBase(t *testing.T) {
	dir := t.TempDir()
	spec := `{"LD_LIBRARY_PATH": {"contents": ["./linux64"], "mode": "append"}}`
	if err := os.WriteFile(filepath.Join(dir, "env.json"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}

	base := []string{"LD_LIBRARY_PATH=/usr/local/lib", "TERM=xterm"}

	// process backend: read and resolve against the same server dir
	got := applyEnvJSON(dir, dir, base)
	if want := "LD_LIBRARY_PATH=/usr/local/lib:" + filepath.Join(dir, "linux64"); !envHas(got, want) {
		t.Fatalf("process backend env %v missing %q", got, want)
	}

	// docker backend: read from the server dir, resolve against the workdir
	got = applyEnvJSON(dir, "/data", base)
	if want := "LD_LIBRARY_PATH=/usr/local/lib:/data/linux64"; !envHas(got, want) {
		t.Fatalf("docker backend env %v missing %q (panel path leaked into container?)", got, want)
	}
	if envContains(got, dir) {
		t.Fatalf("docker backend env %v leaks panel-side path %q into the container", got, dir)
	}
}

func TestApplyEnvJSONMissingFileIsNoop(t *testing.T) {
	base := []string{"A=1"}
	if got := applyEnvJSON(t.TempDir(), "/data", base); len(got) != 1 || got[0] != "A=1" {
		t.Fatalf("applyEnvJSON with no env.json mutated env: %v", got)
	}
}

func TestApplyEnvJSONRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	spec := `{"LD_LIBRARY_PATH": {"contents": ["../outside", "./ok"], "mode": "replace"}}`
	if err := os.WriteFile(filepath.Join(dir, "env.json"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	got := applyEnvJSON(dir, "/data", nil)
	// traversal item dropped, safe item kept (resolved against the workdir)
	if want := "LD_LIBRARY_PATH=/data/ok"; !envHas(got, want) {
		t.Fatalf("env %v missing %q", got, want)
	}
	if envContains(got, "outside") {
		t.Fatalf("env %v accepted a traversal path", got)
	}
}

func envHas(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

func envContains(env []string, substr string) bool {
	for _, kv := range env {
		if strings.Contains(kv, substr) {
			return true
		}
	}
	return false
}
