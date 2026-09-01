package dockerrelay

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Health is what gerry could learn about the local docker daemon. The three
// failure shapes have three different fixes, so they are three fields rather
// than one error: "not installed" is a package manager problem, "permission
// denied" is a group-membership problem, and "not running" is a service
// problem. Fix carries the one-line remedy for whichever applies.
type Health struct {
	Installed bool
	Reachable bool
	// Denied is true when the socket exists but this user may not use it —
	// the classic missing-docker-group case.
	Denied  bool
	Version string
	Fix     string
	Err     string
}

// ProbeDaemon asks docker whether it is usable by this process. It never
// returns an error: an unusable daemon is a diagnosis, not a failure.
func ProbeDaemon(ctx context.Context) Health {
	var h Health
	if _, err := exec.LookPath("docker"); err != nil {
		h.Fix = "install docker (and the compose plugin)"
		return h
	}
	h.Installed = true

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err == nil {
		h.Reachable, h.Version = true, msg
		return h
	}
	h.Err = msg
	switch {
	case strings.Contains(msg, "permission denied"):
		h.Denied = true
		h.Fix = "sudo usermod -aG docker $USER, then log out and back in"
	case strings.Contains(msg, "Cannot connect") || strings.Contains(msg, "daemon running"):
		h.Fix = "start the docker daemon (sudo systemctl enable --now docker)"
	default:
		h.Fix = "check `docker version` by hand"
	}
	return h
}

// GatewayAddrs returns the host-side gateway address of every docker bridge
// network — the addresses containers reach the host on, and what
// host.docker.internal resolves to. Sorted, deduplicated, IPv4 only (docker
// bridges are IPv4 unless explicitly configured otherwise, and an API
// listener on a v6 ULA the containers do not use would be a silent no-op).
func GatewayAddrs(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "network", "ls",
		"--filter", "driver=bridge", "--format", "{{.Name}}").Output()
	if err != nil {
		return nil, fmt.Errorf("docker network ls: %w", err)
	}
	names := strings.Fields(string(out))
	if len(names) == 0 {
		return nil, nil
	}

	// Per-network, so one network gerry cannot inspect does not blind the rest.
	var insp []byte
	for _, n := range names {
		b, err := exec.CommandContext(ctx, "docker", "network", "inspect",
			"--format", "{{json .IPAM.Config}}", n).Output()
		if err == nil {
			insp = append(insp, b...)
		}
	}

	seen := map[string]bool{}
	for _, line := range strings.Split(string(insp), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "null" {
			continue
		}
		var cfgs []struct {
			Gateway string `json:"Gateway"`
		}
		if json.Unmarshal([]byte(line), &cfgs) != nil {
			continue
		}
		for _, c := range cfgs {
			ip := net.ParseIP(c.Gateway)
			if ip == nil || ip.To4() == nil {
				continue
			}
			seen[ip.String()] = true
		}
	}
	addrs := make([]string, 0, len(seen))
	for a := range seen {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	return addrs, nil
}

// IsGateway reports whether host is one of this machine's docker bridge
// gateways. Used to decide that a listener is container-local rather than
// LAN-exposed: docker bridge subnets are not routed off the host.
func IsGateway(ctx context.Context, host string) bool {
	addrs, err := GatewayAddrs(ctx)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if a == host {
			return true
		}
	}
	return false
}

// NetworkExists reports whether a docker network is present. A manifest that
// names a network nothing created is the most common docker-backend failure.
func NetworkExists(ctx context.Context, name string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", "network", "inspect", name).Run() == nil
}

// ResolveOnNetwork reports whether host (a container name or network alias)
// resolves on the given network, and names what it found. This is the check
// that separates "your manifest is wrong" from "your container is down".
func ResolveOnNetwork(ctx context.Context, network, host string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "network", "inspect",
		"--format", "{{range .Containers}}{{.Name}} {{end}}", network).Output()
	if err != nil {
		return "", false
	}
	for _, name := range strings.Fields(string(out)) {
		if name == host {
			return name, true
		}
		aliases, err := exec.CommandContext(ctx, "docker", "inspect", "--format",
			"{{range .NetworkSettings.Networks}}{{range .Aliases}}{{.}} {{end}}{{end}}", name).Output()
		if err != nil {
			continue
		}
		for _, a := range strings.Fields(string(aliases)) {
			if a == host {
				return name, true
			}
		}
	}
	return "", false
}
