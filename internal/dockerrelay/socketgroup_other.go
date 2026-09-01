//go:build !linux

package dockerrelay

// DaemonSocketAccess is the unix-group check described in the linux build.
// Elsewhere the docker socket is not gated on a supplementary group the way
// it is on Linux, so the check reports nothing rather than guessing.
type DaemonSocketAccess struct {
	Known   bool
	Allowed bool
	Reason  string
	Fix     string
}

// DaemonPID is a no-op off Linux.
func DaemonPID() int { return 0 }

// CheckDaemonSocketAccess is a no-op off Linux.
func CheckDaemonSocketAccess(pid int, socket string) DaemonSocketAccess {
	return DaemonSocketAccess{}
}
