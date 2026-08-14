//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// groupCommand runs the child in its own process group and cancels by
// signalling the whole group — Ctrl-C must reach vite's own children.
func groupCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
}
