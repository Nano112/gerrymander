// Package dockerlabels auto-claims hostnames for containers that carry
// gerrymander labels — the Traefik-label pattern, but for local dev:
//
//	services:
//	  api:
//	    labels:
//	      - gerrymander.hostname=api.myapp.test
//	      - gerrymander.port=8080        # optional; default: first exposed port
//	      - gerrymander.network=mynet    # optional; default: first network
//
// `docker compose up` and the hostname exists; `down` and it is released.
// The watcher only ever touches allocations it created itself
// (owner_kind = "docker-label"), so it can never release someone's claim.
package dockerlabels

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/service"
	"github.com/Nano112/gerrymander/internal/store"
)

const (
	labelHostname = "gerrymander.hostname"
	labelPort     = "gerrymander.port"
	labelNetwork  = "gerrymander.network"
	// OwnerKind marks allocations this watcher owns end-to-end.
	OwnerKind = "docker-label"
)

type Watcher struct {
	Store    *store.Store
	Alloc    *service.Alloc
	Interval time.Duration
	Log      *slog.Logger
	// OnMutation fires after any claim/release so the proxy rebuilds now.
	OnMutation func()

	warned map[string]bool // hostnames we already logged a skip for
}

type labeled struct {
	container string
	hostname  string
	network   string
	port      int
}

// Run polls until ctx ends. Docker being down is not an error — the watcher
// just waits for it to come back.
func (w *Watcher) Run(ctx context.Context) {
	if w.Interval <= 0 {
		w.Interval = 5 * time.Second
	}
	w.warned = map[string]bool{}
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	for {
		if err := w.sync(ctx); err != nil && ctx.Err() == nil {
			w.logf("docker-labels: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (w *Watcher) logf(format string, args ...any) {
	if w.Log != nil {
		w.Log.Info(fmt.Sprintf(format, args...))
	}
}

// snapshot lists running containers carrying the hostname label.
func snapshot(ctx context.Context) ([]labeled, error) {
	ids, err := exec.CommandContext(ctx, "docker", "ps", "-q", "--filter", "label="+labelHostname).Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	var out []labeled
	idList := strings.Fields(string(ids))
	if len(idList) == 0 {
		return out, nil
	}
	args := append([]string{"inspect", "--format", "{{json .}}"}, idList...)
	raw, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("docker inspect: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var c struct {
			Name   string `json:"Name"`
			Config struct {
				Labels       map[string]string          `json:"Labels"`
				ExposedPorts map[string]json.RawMessage `json:"ExposedPorts"`
			} `json:"Config"`
			NetworkSettings struct {
				Networks map[string]json.RawMessage `json:"Networks"`
			} `json:"NetworkSettings"`
		}
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			continue
		}
		l := labeled{
			container: strings.TrimPrefix(c.Name, "/"),
			hostname:  strings.ToLower(c.Config.Labels[labelHostname]),
			network:   c.Config.Labels[labelNetwork],
		}
		if p := c.Config.Labels[labelPort]; p != "" {
			l.port, _ = strconv.Atoi(p)
		}
		if l.port == 0 {
			// deterministic default: lowest exposed port
			var ports []int
			for spec := range c.Config.ExposedPorts {
				if n, err := strconv.Atoi(strings.SplitN(spec, "/", 2)[0]); err == nil {
					ports = append(ports, n)
				}
			}
			sort.Ints(ports)
			if len(ports) > 0 {
				l.port = ports[0]
			}
		}
		if l.network == "" {
			var nets []string
			for n := range c.NetworkSettings.Networks {
				nets = append(nets, n)
			}
			sort.Strings(nets)
			if len(nets) > 0 {
				l.network = nets[0]
			}
		}
		if l.hostname != "" && l.port != 0 {
			out = append(out, l)
		}
	}
	return out, nil
}

func (w *Watcher) sync(ctx context.Context) error {
	want, err := snapshot(ctx)
	if err != nil {
		return err
	}
	zones, err := w.Store.ListZones(ctx)
	if err != nil {
		return err
	}

	mutated := false
	desired := map[string]bool{} // fqdn → present
	for _, l := range want {
		desired[l.hostname] = true
		zone, label, ok := splitByZones(l.hostname, zones)
		if !ok {
			if !w.warned[l.hostname] {
				w.warned[l.hostname] = true
				w.logf("docker-labels: %s (%s) matches no registered zone — gerry zone add <zone> first", l.hostname, l.container)
			}
			continue
		}
		existing, err := w.Store.GetAllocationByLabel(ctx, zone, label)
		if err == nil {
			if existing.OwnerKind != OwnerKind {
				if !w.warned[l.hostname] {
					w.warned[l.hostname] = true
					w.logf("docker-labels: %s is already claimed by %s/%s — not touching it", l.hostname, existing.OwnerKind, existing.OwnerRef)
				}
				continue
			}
			if backendMatches(existing, l) {
				continue // steady state
			}
		}
		spec := core.Spec{Routes: []core.Route{{Backend: core.Backend{
			Kind:   "docker",
			Docker: &core.DockerBackend{Network: l.network, Host: l.container, Port: l.port},
		}}}}
		if err == nil {
			// same owner, container details changed: update in place
			existing.Spec = spec
			existing.OwnerRef = "docker:" + l.container
			if uerr := w.Store.UpdateAllocation(ctx, existing); uerr == nil {
				mutated = true
				w.logf("docker-labels: updated %s → %s:%d", l.hostname, l.container, l.port)
			}
			continue
		}
		_, cerr := w.Alloc.Claim(ctx, service.ClaimRequest{
			Zone: zone, Label: label, Kind: core.KindPlatform, Source: core.SourceManifest,
			OwnerRef: "docker:" + l.container, OwnerKind: OwnerKind, Spec: spec,
		})
		if cerr != nil {
			if !w.warned[l.hostname] {
				w.warned[l.hostname] = true
				w.logf("docker-labels: claim %s: %v", l.hostname, cerr)
			}
			continue
		}
		delete(w.warned, l.hostname)
		mutated = true
		w.logf("docker-labels: claimed %s → %s:%d (%s)", l.hostname, l.container, l.port, l.network)
	}

	// release our own claims whose containers are gone
	ours, err := w.Store.ListAllocations(ctx, store.AllocFilter{ExcludeReleased: true})
	if err != nil {
		return err
	}
	for _, a := range ours {
		if a.OwnerKind != OwnerKind || desired[a.FQDN] {
			continue
		}
		if err := w.Alloc.Release(ctx, a.ID); err == nil {
			mutated = true
			w.logf("docker-labels: released %s (container gone)", a.FQDN)
		}
	}

	if mutated && w.OnMutation != nil {
		w.OnMutation()
	}
	return nil
}

func backendMatches(a core.Allocation, l labeled) bool {
	if len(a.Spec.Routes) != 1 {
		return false
	}
	b := a.Spec.Routes[0].Backend
	return b.Kind == "docker" && b.Docker != nil &&
		b.Docker.Host == l.container && b.Docker.Port == l.port && b.Docker.Network == l.network
}

// splitByZones finds the longest registered zone that hostname belongs to.
func splitByZones(hostname string, zones []core.Zone) (zone, label string, ok bool) {
	best := ""
	for _, z := range zones {
		if hostname == z.Name {
			return z.Name, "@", true
		}
		if strings.HasSuffix(hostname, "."+z.Name) && len(z.Name) > len(best) {
			best = z.Name
		}
	}
	if best == "" {
		return "", "", false
	}
	return best, strings.TrimSuffix(hostname, "."+best), true
}
