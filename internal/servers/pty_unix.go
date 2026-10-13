//go:build !windows

package servers

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// ptyAvailable reports PTY support (Unix: always via creack/pty).
func ptyAvailable() bool { return true }

// ptyStart launches cmd attached to a new pseudo-terminal.
func ptyStart(cmd *exec.Cmd) (*os.File, error) {
	return pty.Start(cmd)
}

// setupProcessGroup puts the child into its own process group (signal-safe kills).
func setupProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup sends sig to the child's process group (-pgid).
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		// group may be gone; fall back to direct
		_ = cmd.Process.Signal(sig)
	}
}

// killGroup SIGKILLs the child's whole process tree.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
}

// isWindows is a compile-time constant on Unix.
func isWindows() bool { return false }
