// Package dockerrelay lets a host-mode gerry reach containers that publish
// no ports. For each docker backend it maintains one tiny socat container on
// the target's network, publishing a sticky loopback port — so dockerized
// apps join the estate with zero compose edits.
package dockerrelay

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/service"
)

const relayImage = "alpine/socat"

// Manager ensures relay containers exist and caches their addresses.
type Manager struct {
	Ports *service.Ports
	Log   *slog.Logger

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	addr string
	at   time.Time
}

// NewManager wires a relay manager.
func NewManager(ports *service.Ports) *Manager {
	return &Manager{Ports: ports, Log: slog.Default(), cache: map[string]cached{}}
}

// RelayName is deterministic per (network, host, port) so restarts reuse
// the same container.
func RelayName(d core.DockerBackend) string {
	sum := sha1.Sum([]byte(d.Network + "/" + d.Host + ":" + strconv.Itoa(d.Port)))
	return "gerry-relay-" + hex.EncodeToString(sum[:5])
}

// RelayOwner keys the sticky local port for a relay.
func RelayOwner(d core.DockerBackend) string {
	return "docker-relay:" + d.Network + "/" + d.Host + ":" + strconv.Itoa(d.Port)
}

// Ensure returns a host address ("127.0.0.1:PORT") that reaches the
// container, creating or restarting the relay as needed.
func (m *Manager) Ensure(ctx context.Context, d core.DockerBackend) (string, error) {
	if d.Network == "" || d.Host == "" || d.Port == 0 {
		return "", fmt.Errorf("docker backend needs network, host, and port")
	}
	key := RelayOwner(d)
	m.mu.Lock()
	if c, ok := m.cache[key]; ok && time.Since(c.at) < 15*time.Second {
		m.mu.Unlock()
		return c.addr, nil
	}
	m.mu.Unlock()

	name := RelayName(d)
	// Running already? Trust its published port.
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		`{{if .State.Running}}{{(index (index .NetworkSettings.Ports "4444/tcp") 0).HostPort}}{{end}}`, name).Output()
	if err == nil {
		if port := strings.TrimSpace(string(out)); port != "" {
			addr := "127.0.0.1:" + port
			m.remember(key, addr)
			return addr, nil
		}
	}

	// Claim the sticky loopback port for this relay, then (re)create it.
	pa, err := m.Ports.Claim(ctx, "dev", "", key)
	if err != nil {
		return "", fmt.Errorf("relay port: %w", err)
	}
	exec.CommandContext(ctx, "docker", "rm", "-f", name).Run() // clear stopped remains
	args := []string{
		"run", "-d", "--restart", "unless-stopped", "--name", name,
		"--network", d.Network,
		"-p", fmt.Sprintf("127.0.0.1:%d:4444", pa.Value),
		"--label", "app.gerrymander.relay=true",
		relayImage,
		"TCP-LISTEN:4444,fork,reuseaddr", fmt.Sprintf("TCP:%s:%d", d.Host, d.Port),
	}
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("start relay %s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	m.Log.Info("docker relay started", "relay", name, "network", d.Network,
		"target", fmt.Sprintf("%s:%d", d.Host, d.Port), "local", pa.Value)
	addr := fmt.Sprintf("127.0.0.1:%d", pa.Value)
	m.remember(key, addr)
	return addr, nil
}

func (m *Manager) remember(key, addr string) {
	m.mu.Lock()
	m.cache[key] = cached{addr: addr, at: time.Now()}
	m.mu.Unlock()
}

