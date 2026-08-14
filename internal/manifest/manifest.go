// Package manifest loads gerrymander.yaml project files and applies them to
// the API: hostnames, routes, listeners, supervised services, sticky ports.
package manifest

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Nano112/gerrymander/internal/core"
)

// Manifest is one project's declaration.
type Manifest struct {
	Project  string             `yaml:"project"`
	Zone     string             `yaml:"zone"`
	Services map[string]Service `yaml:"services"`
}

// Service declares hostnames plus a backend.
type Service struct {
	// Hostnames within the zone. "olsyn.test" = apex, "*.olsyn.test" =
	// wildcard, "vite.olsyn.test" = a label. Multiple entries collapse
	// into apex+wildcard on the same allocation when possible.
	Hostnames []string `yaml:"hostnames"`
	// Listen restricts routes to specific proxy ports (default 443/80).
	Listen []int `yaml:"listen,omitempty"`
	// Address backend: "host:port".
	Address string `yaml:"address,omitempty"`
	// PortPool routes to 127.0.0.1:<sticky port for this service>.
	PortPool string `yaml:"port_pool,omitempty"`
	// Supervised backend.
	Supervised *SupervisedSpec `yaml:"supervised,omitempty"`
}

// SupervisedSpec mirrors core.SupervisedBackend in YAML.
type SupervisedSpec struct {
	Cmd         string            `yaml:"cmd"`
	Dir         string            `yaml:"dir"`
	Env         map[string]string `yaml:"env,omitempty"`
	PortPool    string            `yaml:"port_pool,omitempty"`
	IdleTimeout core.Duration     `yaml:"idle_timeout,omitempty"`
	Health      *HealthSpec       `yaml:"health,omitempty"`
}

// HealthSpec mirrors core.HealthCheck.
type HealthSpec struct {
	Path    string        `yaml:"path"`
	Timeout core.Duration `yaml:"timeout,omitempty"`
}

// Load reads and validates a manifest file.
func Load(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if m.Project == "" {
		return nil, fmt.Errorf("%s: project is required", path)
	}
	if m.Zone == "" {
		return nil, fmt.Errorf("%s: zone is required", path)
	}
	for name, svc := range m.Services {
		if len(svc.Hostnames) == 0 {
			return nil, fmt.Errorf("%s: service %q has no hostnames", path, name)
		}
		n := 0
		if svc.Address != "" {
			n++
		}
		if svc.Supervised != nil {
			n++
		}
		if svc.PortPool != "" && svc.Address == "" && svc.Supervised == nil {
			n++
		}
		if n != 1 {
			return nil, fmt.Errorf("%s: service %q needs exactly one backend (address | supervised | port_pool)", path, name)
		}
	}
	return &m, nil
}

// Claim is one desired allocation derived from the manifest.
type Claim struct {
	Zone     string
	Label    string
	Wildcard bool
	Project  string
	OwnerRef string
	Service  string
	Spec     core.Spec
}

// Claims flattens the manifest into allocation requests. resolvePort maps a
// (pool, ownerRef) to a granted sticky port for port_pool references — the
// caller supplies it so claims stay pure.
func (m *Manifest) Claims(resolvePort func(pool, ownerRef string) (int, error)) ([]Claim, error) {
	var out []Claim
	for name, svc := range m.Services {
		ownerRef := m.Project + "/" + name
		backend, err := m.backendFor(name, svc, ownerRef, resolvePort)
		if err != nil {
			return nil, err
		}
		listens := svc.Listen
		if len(listens) == 0 {
			listens = []int{0}
		}
		var routes []core.Route
		for _, l := range listens {
			routes = append(routes, core.Route{Listen: l, Backend: backend})
		}

		// Group hostnames into per-label claims; "x" and "*.x" merge into
		// one claim with Wildcard=true.
		type slot struct {
			wildcard bool
			exact    bool
		}
		labels := map[string]*slot{}
		order := []string{}
		for _, h := range svc.Hostnames {
			label, wildcard, err := m.labelFor(h)
			if err != nil {
				return nil, fmt.Errorf("service %q: %w", name, err)
			}
			s, ok := labels[label]
			if !ok {
				s = &slot{}
				labels[label] = s
				order = append(order, label)
			}
			if wildcard {
				s.wildcard = true
			} else {
				s.exact = true
			}
		}
		for _, label := range order {
			s := labels[label]
			out = append(out, Claim{
				Zone: m.Zone, Label: label, Wildcard: s.wildcard,
				Project: m.Project, OwnerRef: ownerRef, Service: name,
				Spec: core.Spec{Routes: routes, Wildcard: s.wildcard},
			})
		}
	}
	return out, nil
}

func (m *Manifest) backendFor(name string, svc Service, ownerRef string, resolvePort func(pool, ownerRef string) (int, error)) (core.Backend, error) {
	switch {
	case svc.Address != "":
		host, port, err := splitAddr(svc.Address)
		if err != nil {
			return core.Backend{}, fmt.Errorf("service %q: %w", name, err)
		}
		return core.Backend{Kind: "address", Address: &core.AddressBackend{Host: host, Port: port}}, nil
	case svc.Supervised != nil:
		sup := &core.SupervisedBackend{
			Cmd: svc.Supervised.Cmd, Dir: svc.Supervised.Dir, Env: svc.Supervised.Env,
			PortPool: svc.Supervised.PortPool, IdleTimeout: svc.Supervised.IdleTimeout,
		}
		if svc.Supervised.Health != nil {
			sup.Health = &core.HealthCheck{Path: svc.Supervised.Health.Path, Timeout: svc.Supervised.Health.Timeout}
		}
		return core.Backend{Kind: "supervised", Supervised: sup}, nil
	case svc.PortPool != "":
		port, err := resolvePort(svc.PortPool, ownerRef)
		if err != nil {
			return core.Backend{}, fmt.Errorf("service %q port: %w", name, err)
		}
		return core.Backend{Kind: "address", Address: &core.AddressBackend{Host: "host.docker.internal", Port: port}}, nil
	}
	return core.Backend{}, fmt.Errorf("service %q: no backend", name)
}

// labelFor maps a hostname to (label, wildcard) relative to the zone.
func (m *Manifest) labelFor(host string) (string, bool, error) {
	h := strings.ToLower(strings.TrimSpace(host))
	wildcard := strings.HasPrefix(h, "*.")
	h = strings.TrimPrefix(h, "*.")
	if h == m.Zone {
		return "@", wildcard, nil
	}
	if strings.HasSuffix(h, "."+m.Zone) {
		return strings.TrimSuffix(h, "."+m.Zone), wildcard, nil
	}
	return "", false, fmt.Errorf("hostname %q is not in zone %q", host, m.Zone)
}

func splitAddr(addr string) (string, int, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr, 80, nil
	}
	var port int
	if _, err := fmt.Sscanf(addr[i+1:], "%d", &port); err != nil {
		return "", 0, fmt.Errorf("bad address %q", addr)
	}
	return addr[:i], port, nil
}
