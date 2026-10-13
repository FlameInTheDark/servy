// Package servers — shell.go implements the per-server terminal selection:
// how the execution command is launched (wrapped in bash / sh / cmd /
// PowerShell / Git Bash, or exec'd directly). The selection lives on each
// server row (servers.shell):
//
//	""        auto — bash on Unix hosts, cmd.exe on Windows (the platform
//	          default terminal), sh / powershell fallbacks
//	bash      POSIX bash -c (on Windows this resolves to Git Bash if present)
//	sh        POSIX sh -c
//	cmd       cmd.exe /c (batch files: run.bat, start.cmd, …)
//	powershell  powershell.exe -NoProfile -Command
//	pwsh      PowerShell 7+ -NoProfile -Command
//	git-bash   explicit Git for Windows bash.exe -c
//	none      direct exec of the command line (no shell involved)
//
// Hosts report their available shells through DetectShells (panel API
// /panel/shells) so the UI can offer only what actually exists.
package servers

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ShellInfo describes one selectable terminal for the UI.
type ShellInfo struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Path      string `json:"path,omitempty"`
	Available bool   `json:"available"`
	Platform  string `json:"platform"` // "windows", "unix" or "any"
}

// shellKinds enumerates every accepted servers.shell value.
var shellKinds = map[string]bool{
	"": true, "auto": true, "bash": true, "sh": true, "cmd": true,
	"powershell": true, "pwsh": true, "git-bash": true, "none": true,
}

// ValidShellSelection reports whether v is an accepted shell value.
func ValidShellSelection(v string) bool { return shellKinds[v] }

// ShellSpec is a resolved terminal ready to launch a command line.
type ShellSpec struct {
	ID    string   // the selection id
	Kind  string   // resolved launcher kind: bash|sh|cmd|powershell|pwsh|none
	Exe   string   // executable (may be bare name resolved via PATH)
	Args  []string // fixed prefix args ("-c", "/c", "-NoProfile -Command …")
	Label string   // human label
}

// ErrShellUnavailable is returned when an explicitly selected terminal does
// not exist on the host.
var ErrShellUnavailable = errors.New("SHELL_UNAVAILABLE")

// gitBashCandidates lists the usual Git for Windows bash locations.
var gitBashCandidates = []string{
	`C:\Program Files\Git\bin\bash.exe`,
	`C:\Program Files\Git\usr\bin\bash.exe`,
	`C:\Program Files (x86)\Git\bin\bash.exe`,
}

// findExecutable resolves name via PATH (or returns it unchanged when it
// already points at an existing file).
func findExecutable(name string) string {
	if name == "" {
		return ""
	}
	if filepath.IsAbs(name) {
		if _, err := os.Stat(name); err == nil {
			return name
		}
		return ""
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// resolveGitBash finds Git for Windows' bash.exe ("" when absent).
func resolveGitBash() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	for _, p := range gitBashCandidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Git on PATH exposes bash in .../usr/bin next to git.exe
	if git, err := exec.LookPath("git.exe"); err == nil {
		p := filepath.Join(filepath.Dir(filepath.Dir(git)), "usr", "bin", "bash.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// DetectShells reports every terminal selectable on this host.
func DetectShells() []ShellInfo {
	out := []ShellInfo{
		{ID: "auto", Label: "Auto (host default)", Available: true, Platform: "any"},
		{ID: "none", Label: "Direct exec (no shell)", Available: true, Platform: "any"},
	}
	if runtime.GOOS == "windows" {
		comspec := os.Getenv("ComSpec")
		if comspec == "" {
			comspec = `C:\Windows\System32\cmd.exe`
		}
		out = append(out,
			ShellInfo{ID: "cmd", Label: "Command Prompt (cmd.exe)", Path: comspec, Available: true, Platform: "windows"},
			ShellInfo{ID: "powershell", Label: "Windows PowerShell", Path: findExecutable("powershell.exe"), Available: findExecutable("powershell.exe") != "", Platform: "windows"},
			ShellInfo{ID: "pwsh", Label: "PowerShell 7+", Path: findExecutable("pwsh.exe"), Available: findExecutable("pwsh.exe") != "", Platform: "any"},
		)
		if gb := resolveGitBash(); gb != "" {
			out = append(out, ShellInfo{ID: "git-bash", Label: "Git Bash", Path: gb, Available: true, Platform: "windows"})
		} else {
			out = append(out, ShellInfo{ID: "git-bash", Label: "Git Bash (not found)", Available: false, Platform: "windows"})
		}
		out = append(out, ShellInfo{ID: "bash", Label: "bash (via Git Bash)", Path: resolveGitBash(), Available: resolveGitBash() != "", Platform: "windows"})
	} else {
		out = append(out,
			ShellInfo{ID: "bash", Label: "bash", Path: findExecutable("bash"), Available: findExecutable("bash") != "", Platform: "unix"},
			ShellInfo{ID: "sh", Label: "sh", Path: findExecutable("sh"), Available: findExecutable("sh") != "", Platform: "unix"},
			ShellInfo{ID: "pwsh", Label: "PowerShell 7+", Path: findExecutable("pwsh"), Available: findExecutable("pwsh") != "", Platform: "any"},
		)
	}
	return out
}

// DefaultShellID is the auto-resolved terminal for the current host:
// cmd on Windows, bash on Unix (sh fallback).
func DefaultShellID() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	if findExecutable("bash") != "" {
		return "bash"
	}
	return "sh"
}

// ResolveShell turns a servers.shell selection into a launchable spec.
// "" and "auto" resolve to the host default. Unknown/unavailable explicit
// selections return ErrShellUnavailable.
func ResolveShell(selection string) (ShellSpec, error) {
	id := strings.ToLower(strings.TrimSpace(selection))
	switch id {
	case "", "auto":
		id = DefaultShellID()
	}

	spec := ShellSpec{ID: id}
	switch id {
	case "none":
		spec.Kind, spec.Label = "none", "direct exec"
		return spec, nil

	case "bash":
		spec.Kind, spec.Args, spec.Label = "bash", []string{"-c"}, "bash"
		if runtime.GOOS == "windows" {
			// bash on Windows means Git Bash
			if gb := resolveGitBash(); gb != "" {
				spec.Exe = gb
				return spec, nil
			}
			return spec, ErrShellUnavailable
		}
		if p := findExecutable("bash"); p != "" {
			spec.Exe = p
			return spec, nil
		}
		return spec, ErrShellUnavailable

	case "sh":
		spec.Kind, spec.Args, spec.Label = "sh", []string{"-c"}, "sh"
		if p := findExecutable("sh"); p != "" {
			spec.Exe = p
			return spec, nil
		}
		return spec, ErrShellUnavailable

	case "cmd":
		spec.Kind, spec.Args, spec.Label = "cmd", []string{"/c"}, "cmd.exe"
		if runtime.GOOS != "windows" {
			return spec, ErrShellUnavailable
		}
		comspec := os.Getenv("ComSpec")
		if comspec == "" {
			comspec = `C:\Windows\System32\cmd.exe`
		}
		if _, err := os.Stat(comspec); err != nil {
			// last resort: PATH lookup
			comspec = findExecutable("cmd.exe")
			if comspec == "" {
				return spec, ErrShellUnavailable
			}
		}
		spec.Exe = comspec
		return spec, nil

	case "powershell":
		spec.Kind, spec.Label = "powershell", "Windows PowerShell"
		spec.Args = []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command"}
		if p := findExecutable("powershell.exe"); p != "" {
			spec.Exe = p
			return spec, nil
		}
		return spec, ErrShellUnavailable

	case "pwsh":
		spec.Kind, spec.Label = "pwsh", "PowerShell 7+"
		spec.Args = []string{"-NoProfile", "-NonInteractive", "-Command"}
		name := "pwsh"
		if runtime.GOOS == "windows" {
			name = "pwsh.exe"
		}
		if p := findExecutable(name); p != "" {
			spec.Exe = p
			return spec, nil
		}
		return spec, ErrShellUnavailable

	case "git-bash":
		spec.Kind, spec.Args, spec.Label = "bash", []string{"-c"}, "Git Bash"
		if runtime.GOOS != "windows" {
			return spec, ErrShellUnavailable
		}
		if gb := resolveGitBash(); gb != "" {
			spec.Exe = gb
			return spec, nil
		}
		return spec, ErrShellUnavailable
	}
	return spec, ErrShellUnavailable
}

// quotePosixArg single-quotes an argument for bash/sh (-c strings).
func quotePosixArg(a string) string {
	if a == "" {
		return "''"
	}
	if !strings.ContainsAny(a, " \t\n\"'\\$`*&|;<>()?~[]#!") {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// quoteCmdArg escapes an argument following the Windows command-line rules
// (backslash-quote doubling, same as Go's os/exec).
func quoteCmdArg(a string) string {
	if a == "" {
		return `""`
	}
	if !strings.ContainsAny(a, " \t\"") {
		return a
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, r := range a {
		switch r {
		case '\\':
			slashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes*2+1))
			b.WriteByte('"')
			slashes = 0
		default:
			if slashes > 0 {
				b.WriteString(strings.Repeat(`\`, slashes))
				slashes = 0
			}
			b.WriteRune(r)
		}
	}
	b.WriteString(strings.Repeat(`\`, slashes*2))
	b.WriteByte('"')
	return b.String()
}

// JoinArgv rebuilds a shell command line from argv with proper quoting for
// the target shell kind.
func JoinArgv(kind string, argv []string) string {
	quote := quotePosixArg
	if kind == "cmd" {
		quote = quoteCmdArg
	}
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = quote(a)
	}
	return strings.Join(parts, " ")
}

// BuildShellCommand turns the server's execution command (argv form, after
// java selection) into the final exec line for the selected terminal:
//
//	bash/sh/git-bash:  bash -c "<cmd>"
//	cmd:               cmd.exe /c "<cmd>"
//	powershell/pwsh:   powershell -NoProfile -Command "<cmd>"
//	none:              direct argv
//
// The returned args are ready for exec.Command(exe, args...).
func BuildShellCommand(selection string, argv []string) (exe string, args []string, err error) {
	spec, err := ResolveShell(selection)
	if err != nil {
		return "", nil, err
	}
	if spec.Kind == "none" {
		return argv[0], argv[1:], nil
	}
	return spec.Exe, append(spec.Args, JoinArgv(spec.Kind, argv)), nil
}
