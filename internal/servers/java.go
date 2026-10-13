// Package servers — java.go implements JVM detection with legacy parity
// (find_java_installs) with a cross-platform superset, the Minecraft → Java
// requirement matrix used by the creation wizard, and the java_selection
// command rewrite (get_execution_java).
//
// Detection sources (all optional, cached):
//   - "java" on PATH (system default, selection key "java")
//   - JAVA_HOME
//   - /usr/lib/jvm/*            (Debian/Ubuntu images: 8/11/17/21/25)
//   - /opt/java/*, /opt/temurin/*, /opt/openjdk*
//   - update-alternatives --list java   (Debian)  /  archlinux-java status (Arch)
//   - macOS: /Library/Java/JavaVirtualMachines/*/Contents/Home
//   - Windows: Program Files\Java\*, Program Files\Eclipse Adoptium\*, Zulu, Semeru
//   - extra dirs from panel config (java_extra_paths)
package servers

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// JavaInstall is one detected JVM.
type JavaInstall struct {
	Path    string `json:"path"`    // absolute java executable path (the java_selection value)
	Home    string `json:"home"`    // JVM home directory
	Version string `json:"version"` // raw version string (e.g. "17.0.10", "1.8.0_412")
	Major   int    `json:"major"`   // major version (8, 11, 17, 21, 25)
	Vendor  string `json:"vendor"`  // e.g. "Temurin", "Debian", "Oracle"
	Source  string `json:"source"`  // where it was found
}

// MinecraftJavaRequirements documents the Minecraft → Java matrix exposed to
// the UI. Key = range description; value = required major version.
var MinecraftJavaRequirements = []struct {
	Range string `json:"range"`
	Major int    `json:"major"`
}{
	{"<= 1.16.5", 8},
	{"1.17.x", 16},
	{"1.18 – 1.20.4", 17},
	{"1.20.5 – 1.20.6", 21},
	{"1.21+", 21},
}

// SuggestJavaForMinecraft returns the minimum required Java major for a
// Minecraft version string like "1.20.4", "1.21", "1.16.5".
func SuggestJavaForMinecraft(version string) int {
	parts := strings.Split(strings.TrimSpace(strings.ToLower(version)), ".")
	major, minor, patch := 1, 0, 0
	if len(parts) > 0 {
		major = atoiSafe(parts[0])
	}
	if len(parts) > 1 {
		minor = atoiSafe(parts[1])
	}
	if len(parts) > 2 {
		patch = atoiSafe(strings.TrimSuffix(parts[2], "-forge")) // tolerate suffixes
	}
	switch {
	case major > 1:
		return 21 // 2.x and beyond: assume modern
	case minor >= 21:
		return 21
	case minor == 20 && patch >= 5:
		return 21
	case minor >= 18:
		return 17
	case minor == 17:
		return 16
	default:
		return 8
	}
}

// javaDetector caches version probes (they exec the JVM, ~100ms each).
type javaDetector struct {
	mu       sync.Mutex
	byPath   map[string]*JavaInstall
	lastScan time.Time
	extra    []string
}

var jdet = &javaDetector{byPath: map[string]*JavaInstall{}}

// SetExtraJavaPaths registers config-provided extra JVM dirs.
func SetExtraJavaPaths(dirs []string) {
	jdet.mu.Lock()
	defer jdet.mu.Unlock()
	jdet.extra = dirs
	jdet.lastScan = time.Time{} // force rescan
}

// DetectJavas scans every known install location and probes versions.
// Results are cached for 5 minutes.
func DetectJavas() []JavaInstall {
	jdet.mu.Lock()
	if time.Since(jdet.lastScan) < 5*time.Minute {
		cached := make([]JavaInstall, 0, len(jdet.byPath))
		for _, v := range jdet.byPath {
			cached = append(cached, *v)
		}
		jdet.mu.Unlock()
		sort.Slice(cached, func(i, j int) bool { return cached[i].Major > cached[j].Major })
		return cached
	}
	jdet.mu.Unlock()

	found := map[string]string{} // exe path → source
	exeName := "java"
	if runtime.GOOS == "windows" {
		exeName = "java.exe"
	}

	// 1) system default on PATH
	if p, err := exec.LookPath(exeName); err == nil {
		found[p] = "path"
	}

	// 2) JAVA_HOME
	if home := os.Getenv("JAVA_HOME"); home != "" {
		if exe := filepath.Join(home, "bin", exeName); fileExists(exe) {
			found[exe] = "java_home"
		}
	}

	// 3) standard JVM dirs
	var roots []string
	switch runtime.GOOS {
	case "linux":
		roots = []string{"/usr/lib/jvm", "/opt/java", "/opt/temurin", "/usr/java"}
	case "darwin":
		roots = []string{"/Library/Java/JavaVirtualMachines"}
	case "windows":
		roots = []string{
			`C:\Program Files\Java`, `C:\Program Files\Eclipse Adoptium`,
			`C:\Program Files\Zulu`, `C:\Program Files\Semeru`,
		}
	}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			home := filepath.Join(root, e.Name())
			if runtime.GOOS == "darwin" {
				home = filepath.Join(home, "Contents", "Home")
			}
			if exe := filepath.Join(home, "bin", exeName); fileExists(exe) {
				found[exe] = strings.TrimPrefix(root, "/") // e.g. usr/lib/jvm
			}
		}
	}

	// 4) distro alternative systems
	for _, exe := range listAlternativeJavas() {
		if _, ok := found[exe]; !ok {
			found[exe] = "alternatives"
		}
	}

	// 5) config-provided extra dirs
	jdet.mu.Lock()
	extra := append([]string{}, jdet.extra...)
	jdet.mu.Unlock()
	for _, dir := range extra {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if exe := filepath.Join(dir, "bin", exeName); fileExists(exe) {
			found[exe] = "manual"
		} else if fileExists(dir) && strings.HasSuffix(filepath.Base(dir), exeName) {
			found[exe] = "manual"
		}
	}

	// probe versions (bounded concurrency)
	type res struct {
		path string
		inst *JavaInstall
	}
	sem := make(chan struct{}, 8)
	results := make(chan res, len(found))
	var wg sync.WaitGroup
	for path, source := range found {
		wg.Add(1)
		go func(path, source string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			inst := probeJava(path, source)
			results <- res{path, inst}
		}(path, source)
	}
	wg.Wait()
	close(results)

	out := make([]JavaInstall, 0, len(found))
	byPath := map[string]*JavaInstall{}
	for r := range results {
		if r.inst == nil {
			continue
		}
		byPath[r.path] = r.inst
		out = append(out, *r.inst)
	}
	jdet.mu.Lock()
	jdet.byPath = byPath
	jdet.lastScan = time.Now()
	jdet.mu.Unlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Major != out[j].Major {
			return out[i].Major > out[j].Major
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// listAlternativeJavas shells out to update-alternatives / archlinux-java.
func listAlternativeJavas() []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	var out []string
	if ua, err := exec.Command("/usr/bin/update-alternatives", "--list", "java").Output(); err == nil {
		for _, line := range strings.Split(string(ua), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				out = append(out, line)
			}
		}
		return out
	}
	if aj, err := exec.Command("archlinux-java", "status").Output(); err == nil {
		for _, line := range strings.Split(string(aj), "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "java-"))
			if line == "" || strings.Contains(line, "No Java") {
				continue
			}
			exe := filepath.Join("/usr/lib/jvm", strings.TrimPrefix(line, "java-"), "bin", "java")
			if fileExists(exe) {
				out = append(out, exe)
			}
		}
	}
	return out
}

// versionLineRe captures 'openjdk version "17.0.10" 2024-01-16' / vendor lines.
var versionLineRe = regexp.MustCompile(`version "([^"]+)"`)

// probeJava runs `<java> -version` and parses the result.
func probeJava(exePath, source string) *JavaInstall {
	inst := &JavaInstall{Path: exePath, Source: source, Major: -1}
	inst.Home = filepath.Dir(filepath.Dir(exePath))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", exePath+" -version")
	} else {
		cmd = exec.CommandContext(ctx, exePath, "-version")
	}
	raw, _ := cmd.CombinedOutput()
	text := string(raw)
	if m := versionLineRe.FindStringSubmatch(text); m != nil {
		inst.Version = m[1]
	}
	if inst.Version == "" {
		// unprobeable (e.g. wrong-arch binary): still list it, unknown version
		inst.Version = "unknown"
	}
	inst.Major = javaMajor(inst.Version)
	inst.Vendor = javaVendor(text)
	return inst
}

// javaMajor turns "1.8.0_412" → 8 and "17.0.10" → 17.
func javaMajor(version string) int {
	v := strings.TrimSpace(version)
	if v == "" || v == "unknown" {
		return -1
	}
	parts := strings.Split(v, ".")
	if len(parts) > 1 && parts[0] == "1" {
		return atoiSafe(parts[1]) // 1.8 → 8
	}
	return atoiSafe(parts[0])
}

func javaVendor(text string) string {
	switch {
	case strings.Contains(text, "Temurin"):
		return "Temurin"
	case strings.Contains(text, "GraalVM"):
		return "GraalVM"
	case strings.Contains(text, "Zulu"):
		return "Zulu"
	case strings.Contains(text, "Semeru"):
		return "Semeru"
	case strings.Contains(text, "Corretto"):
		return "Corretto"
	case strings.Contains(text, "Debian"):
		return "Debian"
	case strings.Contains(text, "Ubuntu"):
		return "Ubuntu"
	case strings.Contains(text, "Oracle"):
		return "Oracle"
	case strings.Contains(text, "OpenJDK"):
		return "OpenJDK"
	}
	return "unknown"
}

// ApplyJavaSelection rewrites argv[0] of the command according to a
// java_selection value (legacy get_execution_java semantics):
//   - "" / "none"            → unchanged
//   - "java"                 → literal "java" (system default)
//   - absolute executable    → that exact binary
//   - JVM home directory     → <home>/bin/java
//
// For docker-executed servers java runs inside the image, so the selection is
// only applied to the process backend.
func ApplyJavaSelection(argv []string, selection string) []string {
	if len(argv) == 0 {
		return argv
	}
	sel := strings.TrimSpace(selection)
	switch {
	case sel == "", sel == "none":
		return argv
	case sel == "java":
		argv[0] = "java"
		return argv
	}
	if strings.HasSuffix(filepath.Base(sel), "java") || strings.HasSuffix(filepath.Base(sel), "java.exe") {
		argv[0] = sel
		return argv
	}
	exeName := "java"
	if runtime.GOOS == "windows" {
		exeName = "java.exe"
	}
	argv[0] = filepath.Join(sel, "bin", exeName)
	return argv
}

// ValidateJavaSelection checks a user-provided selection against detected
// installs (or "java"/""/"none").
func ValidateJavaSelection(selection string, installs []JavaInstall) bool {
	sel := strings.TrimSpace(selection)
	switch sel {
	case "", "none", "java":
		return true
	}
	for _, j := range installs {
		if j.Path == sel || j.Home == sel {
			return true
		}
	}
	return false
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
