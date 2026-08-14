package proxy

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/store"
)

// Target is a resolved backend for one (host, port) lookup.
type Target struct {
	Alloc   core.Allocation
	Backend core.Backend
}

// entry is one match candidate in the table.
type entry struct {
	fqdn     string // exact fqdn, or the suffix for wildcards
	wildcard bool   // matches "<anything>.<fqdn>"
	depth    int    // dots in fqdn — deeper wins
	alloc    core.Allocation
}

// Table resolves hostnames to backends, rebuilt from the store.
type Table struct {
	mu      sync.RWMutex
	entries []entry
	zones   []string
}

// Zones returns the zone names known at the last rebuild (for error-page
// hints).
func (t *Table) Zones() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]string{}, t.zones...)
}

// Rebuild loads active allocations. Pending/holds do not route.
func (t *Table) Rebuild(ctx context.Context, st *store.Store) error {
	allocs, err := st.ListAllocations(ctx, store.AllocFilter{State: string(core.StateActive)})
	if err != nil {
		return err
	}
	var zoneNames []string
	if zs, err := st.ListZones(ctx); err == nil {
		for _, z := range zs {
			zoneNames = append(zoneNames, z.Name)
		}
	}
	var entries []entry
	for _, a := range allocs {
		if len(a.Spec.Routes) == 0 {
			continue // registry-only allocation; nothing to route
		}
		label := a.Label
		wildcardLabel := strings.HasPrefix(label, "*.")
		if wildcardLabel {
			label = strings.TrimPrefix(label, "*.")
		}
		fqdn := core.FQDN(label, a.ZoneName)
		e := entry{fqdn: fqdn, depth: strings.Count(fqdn, "."), alloc: a}
		if wildcardLabel {
			e.wildcard = true
			entries = append(entries, e)
		} else {
			entries = append(entries, e)
			if a.Spec.Wildcard {
				entries = append(entries, entry{fqdn: fqdn, depth: e.depth, wildcard: true, alloc: a})
			}
		}
	}
	// Exact before wildcard; deeper before shallower.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].wildcard != entries[j].wildcard {
			return !entries[i].wildcard
		}
		return entries[i].depth > entries[j].depth
	})
	t.mu.Lock()
	t.entries = entries
	t.zones = zoneNames
	t.mu.Unlock()
	return nil
}

// Resolve finds the backend for (host, listenPort). listenPort 443/80 match
// the default route (Listen==0); other ports require an explicit route.
func (t *Table) Resolve(host string, listenPort int) (Target, bool) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if i := strings.LastIndex(host, ":"); i > -1 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, e := range t.entries {
		if e.wildcard {
			if !strings.HasSuffix(host, "."+e.fqdn) {
				continue
			}
		} else if host != e.fqdn {
			continue
		}
		if b, ok := routeFor(e.alloc, listenPort); ok {
			return Target{Alloc: e.alloc, Backend: b}, true
		}
		// Host matched but no route for this port — keep scanning (a
		// wildcard sibling may carry the port route).
	}
	return Target{}, false
}

func routeFor(a core.Allocation, listenPort int) (core.Backend, bool) {
	var fallback *core.Backend
	for i := range a.Spec.Routes {
		r := &a.Spec.Routes[i]
		if r.Listen == listenPort {
			return r.Backend, true
		}
		if r.Listen == 0 && (listenPort == 443 || listenPort == 80) {
			fallback = &r.Backend
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return core.Backend{}, false
}

// Watch rebuilds every interval until ctx is done.
func (t *Table) Watch(ctx context.Context, st *store.Store, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.Rebuild(ctx, st)
		}
	}
}
