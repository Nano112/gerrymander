package main

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
)

// tailscaleBin finds the tailscale CLI. The macOS app bundles it without
// putting it on PATH, so that location is checked too.
func tailscaleBin() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	if runtime.GOOS == "darwin" {
		p := "/Applications/Tailscale.app/Contents/MacOS/tailscale"
		if fileExists(p) {
			return p
		}
	}
	return ""
}

// tailscaleIP returns this machine's IPv4 tailnet address, or an error when
// tailscale is absent or logged out.
func tailscaleIP() (net.IP, error) {
	bin := tailscaleBin()
	if bin == "" {
		return nil, fmt.Errorf("tailscale CLI not found")
	}
	out, err := exec.Command(bin, "ip", "-4").Output()
	if err != nil {
		return nil, fmt.Errorf("tailscale ip: %w (logged in?)", err)
	}
	ip := net.ParseIP(strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]))
	if ip == nil {
		return nil, fmt.Errorf("tailscale ip returned no address")
	}
	return ip, nil
}
