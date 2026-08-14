package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
)

// EnsureZone creates a zone if absent and returns it.
func (s *Store) EnsureZone(ctx context.Context, z core.Zone) (core.Zone, error) {
	_, err := s.db.ExecContext(ctx, s.q(`
		INSERT INTO zones (name, profile, wildcard_mode, dns_provider, ingress_provider, policy)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (name) DO NOTHING`),
		z.Name, orDefault(z.Profile, "prod"), boolInt(z.WildcardMode),
		orDefault(z.DNSProvider, "none"), orDefault(z.IngressProvide, "none"),
		orDefault(z.PolicyName, "default"))
	if err != nil {
		return core.Zone{}, err
	}
	return s.GetZone(ctx, z.Name)
}

// GetZone fetches a zone by name.
func (s *Store) GetZone(ctx context.Context, name string) (core.Zone, error) {
	row := s.db.QueryRowContext(ctx, s.q(`
		SELECT id, name, profile, wildcard_mode, dns_provider, ingress_provider, policy, created_at
		FROM zones WHERE name = ?`), name)
	var z core.Zone
	var wc int
	if err := row.Scan(&z.ID, &z.Name, &z.Profile, &wc, &z.DNSProvider, &z.IngressProvide, &z.PolicyName, &z.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return core.Zone{}, fmt.Errorf("zone %q: %w", name, ErrNotFound)
		}
		return core.Zone{}, err
	}
	z.WildcardMode = wc != 0
	return z, nil
}

// ListZones returns all zones.
func (s *Store) ListZones(ctx context.Context) ([]core.Zone, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, profile, wildcard_mode, dns_provider, ingress_provider, policy, created_at FROM zones ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Zone
	for rows.Next() {
		var z core.Zone
		var wc int
		if err := rows.Scan(&z.ID, &z.Name, &z.Profile, &wc, &z.DNSProvider, &z.IngressProvide, &z.PolicyName, &z.CreatedAt); err != nil {
			return nil, err
		}
		z.WildcardMode = wc != 0
		out = append(out, z)
	}
	return out, rows.Err()
}

// CreateAllocation inserts a new allocation. A unique-index rejection maps to
// ErrTaken. This single INSERT is the mutual-exclusion primitive.
func (s *Store) CreateAllocation(ctx context.Context, a core.Allocation) (core.Allocation, error) {
	spec, _ := json.Marshal(a.Spec)
	status, _ := json.Marshal(a.Status)
	labels, _ := json.Marshal(a.Labels)
	if a.State == "" {
		a.State = core.StatePending
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, s.q(`
		INSERT INTO allocations (zone_id, project, label, fqdn, kind, source, owner_ref, owner_kind, state, spec, status, labels, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		a.ZoneID, a.Project, a.Label, a.FQDN, a.Kind, a.Source, a.OwnerRef, a.OwnerKind,
		a.State, string(spec), string(status), string(labels), timePtr(a.ExpiresAt), now, now)
	if err != nil {
		if isUnique(err) {
			return core.Allocation{}, ErrTaken
		}
		return core.Allocation{}, err
	}
	id, err := res.LastInsertId()
	if err == nil && id > 0 {
		a.ID = id
	}
	a.CreatedAt, a.UpdatedAt = now, now
	s.appendEvent(ctx, "allocation", a.ID, "create", "", string(a.State), a.OwnerRef)
	return a, nil
}

// GetAllocation fetches by id.
func (s *Store) GetAllocation(ctx context.Context, id int64) (core.Allocation, error) {
	return s.oneAllocation(ctx, `WHERE a.id = ?`, id)
}

// GetAllocationByLabel fetches by (zone name, label).
func (s *Store) GetAllocationByLabel(ctx context.Context, zone, label string) (core.Allocation, error) {
	return s.oneAllocation(ctx, `WHERE z.name = ? AND a.label = ?`, zone, label)
}

const allocCols = `
	SELECT a.id, a.zone_id, z.name, a.project, a.label, a.fqdn, a.kind, a.source,
	       a.owner_ref, a.owner_kind, a.state, a.spec, a.status, a.labels,
	       a.expires_at, a.created_at, a.updated_at
	FROM allocations a JOIN zones z ON z.id = a.zone_id `

func (s *Store) oneAllocation(ctx context.Context, where string, args ...any) (core.Allocation, error) {
	row := s.db.QueryRowContext(ctx, s.q(allocCols+where), args...)
	a, err := scanAllocation(row)
	if err == sql.ErrNoRows {
		return core.Allocation{}, ErrNotFound
	}
	return a, err
}

type scanner interface{ Scan(...any) error }

func scanAllocation(r scanner) (core.Allocation, error) {
	var a core.Allocation
	var spec, status, labels string
	var exp sql.NullTime
	err := r.Scan(&a.ID, &a.ZoneID, &a.ZoneName, &a.Project, &a.Label, &a.FQDN, &a.Kind, &a.Source,
		&a.OwnerRef, &a.OwnerKind, &a.State, &spec, &status, &labels, &exp, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return a, err
	}
	json.Unmarshal([]byte(spec), &a.Spec)
	json.Unmarshal([]byte(status), &a.Status)
	json.Unmarshal([]byte(labels), &a.Labels)
	if exp.Valid {
		t := exp.Time
		a.ExpiresAt = &t
	}
	return a, nil
}

// AllocFilter narrows ListAllocations.
type AllocFilter struct {
	Zone     string
	Kind     string
	State    string
	OwnerRef string
	Project  string
	// ExcludeReleased drops released rows unless State explicitly asks.
	ExcludeReleased bool
}

// ListAllocations returns allocations matching the filter.
func (s *Store) ListAllocations(ctx context.Context, f AllocFilter) ([]core.Allocation, error) {
	var conds []string
	var args []any
	add := func(c string, v any) { conds = append(conds, c); args = append(args, v) }
	if f.Zone != "" {
		add("z.name = ?", f.Zone)
	}
	if f.Kind != "" {
		add("a.kind = ?", f.Kind)
	}
	if f.State != "" {
		add("a.state = ?", f.State)
	} else if f.ExcludeReleased {
		conds = append(conds, "a.state != 'released'")
	}
	if f.OwnerRef != "" {
		add("a.owner_ref = ?", f.OwnerRef)
	}
	if f.Project != "" {
		add("a.project = ?", f.Project)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	rows, err := s.db.QueryContext(ctx, s.q(allocCols+where+" ORDER BY z.name, a.label"), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Allocation
	for rows.Next() {
		a, err := scanAllocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAllocation persists state/spec/status changes and clears or sets holds.
func (s *Store) UpdateAllocation(ctx context.Context, a core.Allocation) error {
	spec, _ := json.Marshal(a.Spec)
	status, _ := json.Marshal(a.Status)
	labels, _ := json.Marshal(a.Labels)
	_, err := s.db.ExecContext(ctx, s.q(`
		UPDATE allocations SET state = ?, spec = ?, status = ?, labels = ?, expires_at = ?, owner_ref = ?, owner_kind = ?, project = ?, updated_at = ?
		WHERE id = ?`),
		a.State, string(spec), string(status), string(labels), timePtr(a.ExpiresAt), a.OwnerRef, a.OwnerKind, a.Project, time.Now().UTC(), a.ID)
	return err
}

// DeleteAllocation removes a row outright (used for released cleanup; normal
// release is a state transition).
func (s *Store) DeleteAllocation(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM allocations WHERE id = ?`), id)
	return err
}

// ReapExpiredHolds releases pending holds whose expiry has passed. Returns
// the number reaped.
func (s *Store) ReapExpiredHolds(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, s.q(`
		DELETE FROM allocations WHERE state = 'pending' AND expires_at IS NOT NULL AND expires_at < ?`), now.UTC())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *Store) appendEvent(ctx context.Context, subjectType string, subjectID int64, action, from, to string, actor string) {
	s.db.ExecContext(ctx, s.q(`
		INSERT INTO events (subject_type, subject_id, actor, action, from_state, to_state) VALUES (?, ?, ?, ?, ?, ?)`),
		subjectType, subjectID, actor, action, from, to)
}

// Idempotency: Remember stores a response under a key; Recall fetches it.
func (s *Store) RememberIdempotent(ctx context.Context, key, response string) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO idempotency_keys (key, response) VALUES (?, ?) ON CONFLICT (key) DO NOTHING`), key, response)
	return err
}

func (s *Store) RecallIdempotent(ctx context.Context, key string) (string, bool, error) {
	var resp string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT response FROM idempotency_keys WHERE key = ?`), key).Scan(&resp)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return resp, err == nil, err
}

// timePtr flattens *time.Time for drivers that won't dereference pointers.
func timePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
