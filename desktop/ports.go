package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Listener is one live listening TCP socket on this machine.
// Scanning follows port-killer's approach: `lsof -iTCP -sTCP:LISTEN -P -n`.
type Listener struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Command string `json:"command"`
	Addr    string `json:"addr"`
	User    string `json:"user"`
	// Gerry marks ports that are sticky grants in the registry, with the
	// owning ref when known.
	GerryOwner string `json:"gerry_owner,omitempty"`
}

// ScanListeners lists listening TCP ports.
func ScanListeners(ctx context.Context) ([]Listener, error) {
	out, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-iTCP", "-sTCP:LISTEN", "-P", "-n", "+c", "0").Output()
	if err != nil {
		// lsof exits 1 when nothing matches; treat as empty.
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("lsof: %w", err)
	}
	return parseLsof(string(out)), nil
}

// parseLsof extracts one Listener per (pid, port), deduplicating the
// IPv4/IPv6 double entries lsof emits for dual-stack sockets.
func parseLsof(out string) []Listener {
	seen := map[string]bool{}
	var res []Listener
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 9 {
			continue
		}
		pid, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		name := f[len(f)-2] // NAME column (…:port), last is "(LISTEN)"
		if !strings.HasSuffix(f[len(f)-1], "(LISTEN)") {
			name = f[len(f)-1]
		}
		idx := strings.LastIndex(name, ":")
		if idx < 0 {
			continue
		}
		port, err := strconv.Atoi(name[idx+1:])
		if err != nil {
			continue
		}
		key := fmt.Sprintf("%d/%d", pid, port)
		if seen[key] {
			continue
		}
		seen[key] = true
		res = append(res, Listener{
			Port: port, PID: pid, Command: f[0], User: f[2], Addr: name[:idx],
		})
	}
	return res
}

// KillProcess terminates a PID, port-killer style: SIGTERM, grace period,
// then SIGKILL if it still runs. force=true skips straight to SIGKILL.
func KillProcess(pid int, force bool) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to signal pid %d", pid)
	}
	if force {
		return syscall.Kill(pid, syscall.SIGKILL)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		// Signal 0 probes existence.
		if err := syscall.Kill(pid, 0); err != nil {
			return nil // gone
		}
		time.Sleep(100 * time.Millisecond)
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}
