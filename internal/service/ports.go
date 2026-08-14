package service

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/store"
)

// DefaultPoolStart deliberately sits above the congested dev range.
const (
	DefaultPoolStart = 51000
	DefaultPoolEnd   = 59999
)

// DefaultAvoid are ports never granted even inside a configured range.
var DefaultAvoid = []int{3000, 5173, 5432, 6379, 8000, 8080, 9000}

// Binder tests whether a port is actually free on this host. Injectable for
// tests; the default binds 127.0.0.1.
type Binder func(port int) bool

// TCPBinder is the production bind test.
func TCPBinder(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// Ports grants sticky ports out of named pools.
type Ports struct {
	Store *store.Store
	Bind  Binder
	// SkipBindTest disables liveness checks (e.g. server allocating for a
	// different host than itself, as in the prod registry).
	SkipBindTest bool
}

// NewPorts wires the port service.
func NewPorts(st *store.Store) *Ports {
	return &Ports{Store: st, Bind: TCPBinder}
}

// EnsureDefaultPool creates the "dev" pool if missing.
func (p *Ports) EnsureDefaultPool(ctx context.Context) error {
	_, err := p.Store.EnsurePool(ctx, core.PortPool{
		Name: "dev", RangeStart: DefaultPoolStart, RangeEnd: DefaultPoolEnd, Avoid: DefaultAvoid,
	})
	return err
}

// Claim returns the owner's sticky port, allocating one if needed. The grant
// survives restarts; the same owner always gets the same value.
func (p *Ports) Claim(ctx context.Context, poolName, project, ownerRef string) (core.PortAllocation, error) {
	pool, err := p.Store.GetPool(ctx, poolName)
	if err != nil {
		return core.PortAllocation{}, err
	}
	// Sticky path first.
	if existing, err := p.Store.GetPortByOwner(ctx, pool.ID, ownerRef); err == nil {
		if !p.SkipBindTest && p.Bind != nil && !p.Bind(existing.Value) {
			// Something unmanaged holds it. Report, never reassign silently.
			p.Store.MarkPortState(ctx, existing.ID, "occupied-foreign")
			existing.State = "occupied-foreign"
		}
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return core.PortAllocation{}, err
	}

	avoid := map[int]bool{}
	for _, v := range pool.Avoid {
		avoid[v] = true
	}
	used, err := p.Store.UsedPorts(ctx, pool.ID)
	if err != nil {
		return core.PortAllocation{}, err
	}
	for v := pool.RangeStart; v <= pool.RangeEnd; v++ {
		if avoid[v] || used[v] {
			continue
		}
		if !p.SkipBindTest && p.Bind != nil && !p.Bind(v) {
			continue // squatted by an unmanaged process; skip
		}
		pa, err := p.Store.InsertPort(ctx, pool.ID, project, ownerRef, v)
		if errors.Is(err, store.ErrTaken) {
			// Lost a race for this value (or the owner won one elsewhere) —
			// re-check sticky, then continue the walk.
			if existing, gerr := p.Store.GetPortByOwner(ctx, pool.ID, ownerRef); gerr == nil {
				return existing, nil
			}
			continue
		}
		if err != nil {
			return core.PortAllocation{}, err
		}
		pa.PoolName = pool.Name
		return pa, nil
	}
	return core.PortAllocation{}, fmt.Errorf("pool %q exhausted", poolName)
}

// Release frees the owner's grant in a pool.
func (p *Ports) Release(ctx context.Context, poolName, ownerRef string) error {
	pool, err := p.Store.GetPool(ctx, poolName)
	if err != nil {
		return err
	}
	return p.Store.ReleasePort(ctx, pool.ID, ownerRef)
}
