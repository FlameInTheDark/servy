package servers

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestValidShellSelection(t *testing.T) {
	for _, v := range []string{"", "auto", "bash", "sh", "cmd", "powershell", "pwsh", "git-bash", "none"} {
		if !ValidShellSelection(v) {
			t.Errorf("ValidShellSelection(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"fish", "zsh", "BASH", "cmd.exe", "random"} {
		if ValidShellSelection(v) {
			t.Errorf("ValidShellSelection(%q) = true, want false", v)
		}
	}
}

func TestResolveShellNone(t *testing.T) {
	spec, err := ResolveShell("none")
	if err != nil {
		t.Fatalf("none: %v", err)
	}
	if spec.Kind != "none" {
		t.Fatalf("none kind = %q", spec.Kind)
	}
}

func TestResolveShellAuto(t *testing.T) {
	spec, err := ResolveShell("")
	if err != nil {
		t.Fatalf("auto: %v", err)
	}
	if runtime.GOOS == "windows" {
		if spec.Kind != "cmd" {
			t.Errorf("windows auto = %q, want cmd", spec.Kind)
		}
	} else {
		if spec.Kind != "bash" && spec.Kind != "sh" {
			t.Errorf("unix auto = %q, want bash or sh", spec.Kind)
		}
	}
}

func TestResolveShellUnavailable(t *testing.T) {
	if runtime.GOOS != "windows" {
		if _, err := ResolveShell("cmd"); err != ErrShellUnavailable {
			t.Errorf("cmd on unix: err = %v, want ErrShellUnavailable", err)
		}
		if _, err := ResolveShell("git-bash"); err != ErrShellUnavailable {
			t.Errorf("git-bash on unix: err = %v, want ErrShellUnavailable", err)
		}
	}
}

func TestJoinArgvPosixQuoting(t *testing.T) {
	got := JoinArgv("bash", []string{"java", "-Xmx1G", "-jar", "my server.jar", "nogui"})
	want := "java -Xmx1G -jar 'my server.jar' nogui"
	if got != want {
		t.Errorf("posix join = %q, want %q", got, want)
	}
	// single quotes inside args must be escaped
	got = JoinArgv("bash", []string{"echo", "it's fine"})
	want = "echo 'it'\\''s fine'"
	if got != want {
		t.Errorf("posix quote escape = %q, want %q", got, want)
	}
}

func TestJoinArgvCmdQuoting(t *testing.T) {
	got := JoinArgv("cmd", []string{"C:\\Program Files\\Java\\bin\\java.exe", "-jar", "server.jar"})
	want := `"C:\Program Files\Java\bin\java.exe" -jar server.jar`
	if got != want {
		t.Errorf("cmd join = %q, want %q", got, want)
	}
}

func TestBuildShellCommandDirect(t *testing.T) {
	exe, args, err := BuildShellCommand("none", []string{"java", "-jar", "server.jar"})
	if err != nil {
		t.Fatal(err)
	}
	if exe != "java" || strings.Join(args, " ") != "-jar server.jar" {
		t.Errorf("direct = %q %v", exe, args)
	}
}

func TestBuildShellCommandBashExecutes(t *testing.T) {
	// On Unix the auto shell is bash/sh — verify the wrapped command line
	// actually executes (shell metacharacters included, args with spaces).
	if runtime.GOOS == "windows" {
		t.Skip("unix-only")
	}
	exe, args, err := BuildShellCommand("", []string{"echo", "hello world", "&&", "echo", "done"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(exe, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("wrapped command failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "hello world") || !strings.Contains(string(out), "done") {
		t.Errorf("wrapped output = %q, want both echoes", out)
	}
}

func TestBuildShellCommandJavaSelectionQuoted(t *testing.T) {
	// A java path with spaces must survive re-quoting into the shell line.
	if runtime.GOOS == "windows" {
		t.Skip("posix quoting check")
	}
	argv := ApplyJavaSelection([]string{"java", "-jar", "server.jar"}, "/opt/java with spaces/bin/java")
	exe, args, err := BuildShellCommand("bash", argv)
	if err != nil {
		t.Fatal(err)
	}
	if exe == "" || len(args) < 2 || args[0] != "-c" {
		t.Fatalf("bash wrap = %q %v", exe, args)
	}
	if !strings.Contains(args[len(args)-1], `'/opt/java with spaces/bin/java' -jar server.jar`) {
		t.Errorf("bash -c line = %q, want quoted java path", args[len(args)-1])
	}
}

func TestDetectShells(t *testing.T) {
	shells := DetectShells()
	ids := map[string]bool{}
	for _, s := range shells {
		ids[s.ID] = true
		if s.ID == "auto" || s.ID == "none" {
			if !s.Available {
				t.Errorf("%s must always be available", s.ID)
			}
		}
	}
	if !ids["auto"] || !ids["none"] {
		t.Errorf("auto/none missing from DetectShells: %v", ids)
	}
	if runtime.GOOS == "windows" {
		if !ids["cmd"] || !ids["powershell"] {
			t.Errorf("windows shells missing: %v", ids)
		}
	} else {
		if !ids["bash"] || !ids["sh"] {
			t.Errorf("unix shells missing: %v", ids)
		}
	}
}
