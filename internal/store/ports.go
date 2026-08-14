package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
)

// EnsurePool creates a port pool if absent.
func (s *Store) EnsurePool(ctx context.Context, p core.PortPool) (core.PortPool, error) {
	avoid, _ := json.Marshal(p.Avoid)
	_, err := s.db.ExecContext(ctx, s.q(`
		INSERT INTO port_pools (name, range_start, range_end, avoid) VALUES (?, ?, ?, ?)
		ON CONFLICT (name) DO NOTHING`), p.Name, p.RangeStart, p.RangeEnd, string(avoid))
	if err != nil {
		return core.PortPool{}, err
	}
	return s.GetPool(ctx, p.Name)
}

// GetPool fetches a pool by name.
func (s *Store) GetPool(ctx context.Context, name string) (core.PortPool, error) {
	row := s.db.QueryRowContext(ctx, s.q(`SELECT id, name, range_start, range_end, avoid FROM port_pools WHERE name = ?`), name)
	var p core.PortPool
	var avoid string
	if err := row.Scan(&p.ID, &p.Name, &p.RangeStart, &p.RangeEnd, &avoid); err != nil {
		if err == sql.ErrNoRows {
			return core.PortPool{}, fmt.Errorf("pool %q: %w", name, ErrNotFound)
		}
		return core.PortPool{}, err
	}
	json.Unmarshal([]byte(avoid), &p.Avoid)
	return p, nil
}

// GetPortByOwner returns the sticky allocation for (pool, owner), if any.
func (s *Store) GetPortByOwner(ctx context.Context, poolID int64, ownerRef string) (core.PortAllocation, error) {
	row := s.db.QueryRowContext(ctx, s.q(`
		SELECT pa.id, pa.pool_id, pp.name, pa.project, pa.value, pa.owner_ref, pa.state, pa.last_verified_at
		FROM port_allocations pa JOIN port_pools pp ON pp.id = pa.pool_id
		WHERE pa.pool_id = ? AND pa.owner_ref = ?`), poolID, ownerRef)
	var pa core.PortAllocation
	err := row.Scan(&pa.ID, &pa.PoolID, &pa.PoolName, &pa.Project, &pa.Value, &pa.OwnerRef, &pa.State, &pa.LastVerifiedAt)
	if err == sql.ErrNoRows {
		return core.PortAllocation{}, ErrNotFound
	}
	return pa, err
}

// UsedPorts returns the set of values already allocated in a pool.
func (s *Store) UsedPorts(ctx context.Context, poolID int64) (map[int]bool, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT value FROM port_allocations WHERE pool_id = ?`), poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	used := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		used[v] = true
	}
	return used, rows.Err()
}

// InsertPort inserts one (pool, owner, value) grant. Unique violations map
// to ErrTaken — the caller retries with the next candidate.
func (s *Store) InsertPort(ctx context.Context, poolID int64, project, ownerRef string, value int) (core.PortAllocation, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, s.q(`
		INSERT INTO port_allocations (pool_id, project, value, owner_ref, state, last_verified_at)
		VALUES (?, ?, ?, ?, 'active', ?)`), poolID, project, value, ownerRef, now)
	if err != nil {
		if isUnique(err) {
			return core.PortAllocation{}, ErrTaken
		}
		return core.PortAllocation{}, err
	}
	id, _ := res.LastInsertId()
	return core.PortAllocation{ID: id, PoolID: poolID, Project: project, Value: value, OwnerRef: ownerRef, State: "active", LastVerifiedAt: now}, nil
}

// ListPorts lists allocations, optionally filtered by pool name.
func (s *Store) ListPorts(ctx context.Context, pool string) ([]core.PortAllocation, error) {
	q := `SELECT pa.id, pa.pool_id, pp.name, pa.project, pa.value, pa.owner_ref, pa.state, pa.last_verified_at
	      FROM port_allocations pa JOIN port_pools pp ON pp.id = pa.pool_id`
	var args []any
	if pool != "" {
		q += ` WHERE pp.name = ?`
		args = append(args, pool)
	}
	q += ` ORDER BY pa.value`
	rows, err := s.db.QueryContext(ctx, s.q(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.PortAllocation
	for rows.Next() {
		var pa core.PortAllocation
		if err := rows.Scan(&pa.ID, &pa.PoolID, &pa.PoolName, &pa.Project, &pa.Value, &pa.OwnerRef, &pa.State, &pa.LastVerifiedAt); err != nil {
			return nil, err
		}
		out = append(out, pa)
	}
	return out, rows.Err()
}

// ReleasePort removes a sticky grant.
func (s *Store) ReleasePort(ctx context.Context, poolID int64, ownerRef string) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM port_allocations WHERE pool_id = ? AND owner_ref = ?`), poolID, ownerRef)
	return err
}

// MarkPortState flags e.g. "occupied-foreign" after a failed bind test.
func (s *Store) MarkPortState(ctx context.Context, id int64, state string) error {
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE port_allocations SET state = ?, last_verified_at = ? WHERE id = ?`), state, time.Now().UTC(), id)
	return err
}
