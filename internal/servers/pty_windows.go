//go:build windows

package servers

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// ptyAvailable reports PTY support. ConPTY integration is planned; v1 falls
// back to pipes on Windows (documented in docs/04-process-management.md §3.6).
func ptyAvailable() bool { return false }

// ptyStart is unused on Windows (pipes fallback).
func ptyStart(cmd *exec.Cmd) (*os.File, error) {
	return nil, fmt.Errorf("PTY not supported on Windows in this build")
}

// setupProcessGroup is a no-op on Windows (Job Objects planned).
func setupProcessGroup(cmd *exec.Cmd) {}

// signalGroup sends sig to the child (best effort).
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(sig)
}

// killGroup uses taskkill /T /F to take down the tree.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(cmd.Process.Pid)).Run()
}

// isWindows is a compile-time constant on Windows.
func isWindows() bool { return true }
