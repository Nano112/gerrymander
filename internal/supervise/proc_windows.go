//go:build windows

package supervise

import (
	"os/exec"
	"strconv"
)

// Windows has no POSIX process groups; taskkill /T walks the child tree.
func setProcessGroup(cmd *exec.Cmd) {}

func killGroup(pid int) {
	exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}

func terminateGroup(pid int) {
	// no SIGTERM equivalent; a tree-kill is the honest behavior here
	exec.Command("taskkill", "/T", "/PID", strconv.Itoa(pid)).Run()
}
