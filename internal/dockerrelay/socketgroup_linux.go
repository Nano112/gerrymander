//go:build linux

package dockerrelay

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// DaemonSocketAccess reports whether the running gerry daemon can use the
// docker socket, and why not when it cannot.
//
// This exists because the CLI and the daemon can disagree. On Linux, adding
// yourself to the docker group takes effect at your next login: a shell
// started afterwards (or one that ran `newgrp docker`) has the group, while
// the systemd --user manager that supervises gerry was started before it and
// does not. `gerry status` then reports docker healthy — from its own
// process — while every relay fails with "permission denied" and every
// docker-backed hostname 502s.
//
// The check is deliberately a group comparison rather than a probe: the
// daemon offers no endpoint to ask, and reading its supplementary groups from
// /proc is exact. Anything it cannot determine is reported as "unknown"
// rather than as a failure, so this never invents a problem.
type DaemonSocketAccess struct {
	Known   bool
	Allowed bool
	Reason  string
	Fix     string
}

// CheckDaemonSocketAccess inspects the process listed in pid (the gerry
// daemon) against the docker socket's ownership.
func CheckDaemonSocketAccess(pid int, socket string) DaemonSocketAccess {
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	fi, err := os.Stat(socket)
	if err != nil {
		return DaemonSocketAccess{} // no socket: not this check's problem
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return DaemonSocketAccess{}
	}
	// World-writable socket: group membership is irrelevant.
	if fi.Mode().Perm()&0o006 == 0o006 {
		return DaemonSocketAccess{Known: true, Allowed: true}
	}

	groups, err := processGroups(pid)
	if err != nil {
		return DaemonSocketAccess{}
	}
	for _, g := range groups {
		if g == int(st.Gid) {
			return DaemonSocketAccess{Known: true, Allowed: true}
		}
	}
	// Root can always use it, group or not.
	if uid, err := processUID(pid); err == nil && uid == 0 {
		return DaemonSocketAccess{Known: true, Allowed: true}
	}

	return DaemonSocketAccess{
		Known:  true,
		Reason: fmt.Sprintf("the daemon (pid %d) is not in group %d, which owns %s", pid, st.Gid, socket),
		Fix:    "log out and back in — a --user service keeps the groups it had at login, so restarting it is not enough",
	}
}

// DaemonPID finds the local `gerry serve` process, or 0. Used to check the
// daemon's docker access rather than the CLI's — they are different processes
// with different groups, which is the whole point of the check.
func DaemonPID() int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	self := os.Getpid()
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		if len(args) < 2 || !strings.HasSuffix(args[0], "gerry") {
			continue
		}
		if args[1] == "serve" {
			return pid
		}
	}
	return 0
}

func processGroups(pid int) ([]int, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		rest, ok := strings.CutPrefix(line, "Groups:")
		if !ok {
			continue
		}
		var out []int
		for _, f := range strings.Fields(rest) {
			if n, err := strconv.Atoi(f); err == nil {
				out = append(out, n)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("no Groups line in /proc/%d/status", pid)
}

func processUID(pid int) (int, error) {
	fi, err := os.Stat("/proc/" + strconv.Itoa(pid))
	if err != nil {
		return -1, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, fmt.Errorf("no stat for /proc/%d", pid)
	}
	return int(st.Uid), nil
}
