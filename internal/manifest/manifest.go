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

// Service declares hostnames plus one or more routes.
type Service struct {
	// Hostnames within the zone. "olsyn.test" = apex, "*.olsyn.test" =
	// wildcard, "vite.olsyn.test" = a label. Multiple entries collapse
	// into apex+wildcard on the same allocation when possible.
	Hostnames []string `yaml:"hostnames"`
	// Listen restricts the shorthand backend to specific proxy ports
	// (default 443/80).
	Listen []int `yaml:"listen,omitempty"`
	// Shorthand single backend: exactly one of these — or use Routes.
	Address    string          `yaml:"address,omitempty"`
	PortPool   string          `yaml:"port_pool,omitempty"`
	Supervised *SupervisedSpec `yaml:"supervised,omitempty"`
	// Routes declares per-listener backends when one hostname serves
	// different upstreams on different ports (e.g. app on 443, Vite HMR
	// on :5175).
	Routes []RouteSpec `yaml:"routes,omitempty"`
}

// RouteSpec is one listener→backend binding inside a service.
type RouteSpec struct {
	Listen     int             `yaml:"listen,omitempty"` // 0 = default 443/80
	Address    string          `yaml:"address,omitempty"`
	PortPool   string          `yaml:"port_pool,omitempty"`
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
	return Parse(b, path)
}

// Parse validates manifest bytes (name is used in error messages only).
func Parse(b []byte, path string) (*Manifest, error) {
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
		if len(svc.Routes) > 0 {
			if n != 0 {
				return nil, fmt.Errorf("%s: service %q mixes routes with a shorthand backend", path, name)
			}
			for i, r := range svc.Routes {
				rn := 0
				if r.Address != "" {
					rn++
				}
				if r.Supervised != nil {
					rn++
				}
				if r.PortPool != "" && r.Address == "" && r.Supervised == nil {
					rn++
				}
				if rn != 1 {
					return nil, fmt.Errorf("%s: service %q route %d needs exactly one backend", path, name, i)
				}
			}
		} else if n != 1 {
			return nil, fmt.Errorf("%s: service %q needs exactly one backend (address | supervised | port_pool | routes)", path, name)
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
	claimedLabels := map[string]string{} // label → service, cross-service collision check
	for name, svc := range m.Services {
		ownerRef := m.Project + "/" + name
		var routes []core.Route
		if len(svc.Routes) > 0 {
			for i, rs := range svc.Routes {
				backend, err := backendFrom(fmt.Sprintf("%s route %d", name, i), rs.Address, rs.PortPool, rs.Supervised, ownerRef, resolvePort)
				if err != nil {
					return nil, err
				}
				routes = append(routes, core.Route{Listen: rs.Listen, Backend: backend})
			}
		} else {
			backend, err := backendFrom(name, svc.Address, svc.PortPool, svc.Supervised, ownerRef, resolvePort)
			if err != nil {
				return nil, err
			}
			listens := svc.Listen
			if len(listens) == 0 {
				listens = []int{0}
			}
			for _, l := range listens {
				routes = append(routes, core.Route{Listen: l, Backend: backend})
			}
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
			if prev, dup := claimedLabels[label]; dup {
				return nil, fmt.Errorf("services %q and %q both claim label %q — merge them into one service with routes", prev, name, label)
			}
			claimedLabels[label] = name
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

func backendFrom(name, address, portPool string, sup *SupervisedSpec, ownerRef string, resolvePort func(pool, ownerRef string) (int, error)) (core.Backend, error) {
	switch {
	case address != "":
		host, port, err := splitAddr(address)
		if err != nil {
			return core.Backend{}, fmt.Errorf("service %q: %w", name, err)
		}
		return core.Backend{Kind: "address", Address: &core.AddressBackend{Host: host, Port: port}}, nil
	case sup != nil:
		s := &core.SupervisedBackend{
			Cmd: sup.Cmd, Dir: sup.Dir, Env: sup.Env,
			PortPool: sup.PortPool, IdleTimeout: sup.IdleTimeout,
		}
		if sup.Health != nil {
			s.Health = &core.HealthCheck{Path: sup.Health.Path, Timeout: sup.Health.Timeout}
		}
		return core.Backend{Kind: "supervised", Supervised: s}, nil
	case portPool != "":
		port, err := resolvePort(portPool, ownerRef)
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
