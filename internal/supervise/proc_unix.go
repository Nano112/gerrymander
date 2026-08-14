//go:build !windows

package supervise

import (
	"os/exec"
	"syscall"
)

// setProcessGroup makes the child its own process group leader so the whole
// tree can be signalled at once.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(pid int)      { syscall.Kill(-pid, syscall.SIGKILL) }
func terminateGroup(pid int) { syscall.Kill(-pid, syscall.SIGTERM) }
