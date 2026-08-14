//go:build windows

package main

import (
	"os/exec"
	"strconv"
)

// Windows: taskkill /T fells the child tree; no POSIX groups.
func groupCommand(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
}
