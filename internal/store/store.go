// Package store persists gerrymander's state. SQLite is the primary engine
// (pure-Go driver, fine for single-node prod); a Postgres DSN is accepted and
// uses the same portable SQL with rebound placeholders.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// ErrTaken is returned when a unique constraint rejects a claim.
var ErrTaken = errors.New("already allocated")

// ErrNotFound is returned when a lookup misses.
var ErrNotFound = errors.New("not found")

// Store wraps the database. All methods are safe for concurrent use.
type Store struct {
	db       *sql.DB
	postgres bool
}

// Open connects and migrates. DSNs:
//
//	sqlite:/path/to/gerry.db   (or a bare path)
//	postgres://user:pw@host/db
func Open(dsn string) (*Store, error) {
	var (
		db  *sql.DB
		pg  bool
		err error
	)
	switch {
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"):
		db, err = sql.Open("pgx", dsn)
		pg = true
	default:
		path := strings.TrimPrefix(dsn, "sqlite:")
		// busy_timeout + WAL make SQLite behave under concurrent writers.
		db, err = sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	}
	if err != nil {
		return nil, err
	}
	if !pg {
		// modernc/sqlite serializes writes; a single conn avoids
		// SQLITE_BUSY storms without hurting this workload.
		db.SetMaxOpenConns(1)
	}
	s := &Store{db: db, postgres: pg}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// q rebinds ?-placeholders to $N for Postgres.
func (s *Store) q(query string) string {
	if !s.postgres {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isUnique reports whether err is a unique-constraint violation on either
// engine.
func isUnique(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || // sqlite
		strings.Contains(msg, "SQLSTATE 23505") || strings.Contains(msg, "duplicate key") // postgres
}

var migrations = []string{`
CREATE TABLE IF NOT EXISTS zones (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	profile TEXT NOT NULL DEFAULT 'prod',
	wildcard_mode INTEGER NOT NULL DEFAULT 1,
	dns_provider TEXT NOT NULL DEFAULT 'none',
	ingress_provider TEXT NOT NULL DEFAULT 'none',
	policy TEXT NOT NULL DEFAULT 'default',
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS allocations (
	id INTEGER PRIMARY KEY,
	zone_id INTEGER NOT NULL REFERENCES zones(id),
	project TEXT NOT NULL DEFAULT '',
	label TEXT NOT NULL,
	fqdn TEXT NOT NULL,
	kind TEXT NOT NULL,
	source TEXT NOT NULL,
	owner_ref TEXT NOT NULL DEFAULT '',
	owner_kind TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL,
	spec TEXT NOT NULL DEFAULT '{}',
	status TEXT NOT NULL DEFAULT '{}',
	labels TEXT NOT NULL DEFAULT '{}',
	expires_at TIMESTAMP,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE (zone_id, label)
);
CREATE TABLE IF NOT EXISTS port_pools (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	range_start INTEGER NOT NULL,
	range_end INTEGER NOT NULL,
	avoid TEXT NOT NULL DEFAULT '[]'
);
CREATE TABLE IF NOT EXISTS port_allocations (
	id INTEGER PRIMARY KEY,
	pool_id INTEGER NOT NULL REFERENCES port_pools(id),
	project TEXT NOT NULL DEFAULT '',
	value INTEGER NOT NULL,
	owner_ref TEXT NOT NULL,
	state TEXT NOT NULL DEFAULT 'active',
	last_verified_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE (pool_id, value),
	UNIQUE (pool_id, owner_ref)
);
CREATE TABLE IF NOT EXISTS events (
	id INTEGER PRIMARY KEY,
	subject_type TEXT NOT NULL,
	subject_id INTEGER NOT NULL,
	actor TEXT NOT NULL DEFAULT '',
	action TEXT NOT NULL,
	from_state TEXT NOT NULL DEFAULT '',
	to_state TEXT NOT NULL DEFAULT '',
	payload TEXT NOT NULL DEFAULT '{}',
	at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS idempotency_keys (
	key TEXT PRIMARY KEY,
	response TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_alloc_owner ON allocations(owner_ref);
CREATE INDEX IF NOT EXISTS idx_alloc_state ON allocations(state);
CREATE INDEX IF NOT EXISTS idx_events_subject ON events(subject_type, subject_id);
`}

// migrate runs every migration past the DB's recorded schema version.
// Versioning: SQLite uses PRAGMA user_version; Postgres a schema_migrations
// row. Migration 1 (the baseline) is written with IF NOT EXISTS throughout,
// so pre-versioning databases adopt version 1 without change.
func (s *Store) migrate(ctx context.Context) error {
	current, err := s.schemaVersion(ctx)
	if err != nil {
		return err
	}
	for v := current; v < len(migrations); v++ {
		ddl := migrations[v]
		if s.postgres {
			ddl = strings.ReplaceAll(ddl, "INTEGER PRIMARY KEY", "BIGSERIAL PRIMARY KEY")
		}
		for _, stmt := range strings.Split(ddl, ";") {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if _, err := s.db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("migration %d %q: %w", v+1, stmt[:min(len(stmt), 60)], err)
			}
		}
		if err := s.setSchemaVersion(ctx, v+1); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) schemaVersion(ctx context.Context) (int, error) {
	if s.postgres {
		if _, err := s.db.ExecContext(ctx,
			"CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER NOT NULL)"); err != nil {
			return 0, err
		}
		var v int
		err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&v)
		return v, err
	}
	var v int
	err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

func (s *Store) setSchemaVersion(ctx context.Context, v int) error {
	if s.postgres {
		_, err := s.db.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", v)
		return err
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v))
	return err
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
