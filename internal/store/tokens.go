package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// Token is a scoped API credential. The plaintext is shown once at creation;
// only its SHA-256 is stored.
type Token struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope"` // "admin" | "owner"
	OwnerRef   string     `json:"owner_ref,omitempty"`
	Zones      []string   `json:"zones,omitempty"` // empty = all zones
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// ScopeAdmin tokens can do everything the root API key can.
// ScopeOwner tokens are confined to tenant claims for one owner_ref.
const (
	ScopeAdmin = "admin"
	ScopeOwner = "owner"
)

func hashToken(plaintext string) string {
	h := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(h[:])
}

// CreateToken mints a token and returns its plaintext exactly once.
func (s *Store) CreateToken(ctx context.Context, name, scope, ownerRef string, zones []string) (Token, string, error) {
	if scope != ScopeAdmin && scope != ScopeOwner {
		return Token{}, "", errors.New("scope must be admin or owner")
	}
	if scope == ScopeOwner && ownerRef == "" {
		return Token{}, "", errors.New("owner-scoped tokens need an owner_ref")
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return Token{}, "", err
	}
	plaintext := "gk_" + hex.EncodeToString(raw)
	zs, _ := json.Marshal(zones)
	res, err := s.db.ExecContext(ctx, s.q(
		"INSERT INTO tokens (name, hash, scope, owner_ref, zones) VALUES (?, ?, ?, ?, ?)"),
		name, hashToken(plaintext), scope, ownerRef, string(zs))
	if err != nil {
		if isUnique(err) {
			return Token{}, "", ErrTaken
		}
		return Token{}, "", err
	}
	id, _ := res.LastInsertId()
	return Token{ID: id, Name: name, Scope: scope, OwnerRef: ownerRef, Zones: zones, CreatedAt: time.Now()}, plaintext, nil
}

// LookupToken resolves a presented plaintext to its live (unrevoked) token.
func (s *Store) LookupToken(ctx context.Context, plaintext string) (Token, error) {
	row := s.db.QueryRowContext(ctx, s.q(
		"SELECT id, name, scope, owner_ref, zones, created_at, last_used_at, revoked_at FROM tokens WHERE hash = ?"),
		hashToken(plaintext))
	t, err := scanToken(row)
	if err != nil {
		return Token{}, err
	}
	if t.RevokedAt != nil {
		return Token{}, ErrNotFound
	}
	// last_used is advisory; failures don't block auth.
	s.db.ExecContext(ctx, s.q("UPDATE tokens SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?"), t.ID)
	return t, nil
}

// ListTokens returns all tokens, revoked included (metadata only).
func (s *Store) ListTokens(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, name, scope, owner_ref, zones, created_at, last_used_at, revoked_at FROM tokens ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken disables a token by name. Revocation is permanent.
func (s *Store) RevokeToken(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, s.q(
		"UPDATE tokens SET revoked_at = CURRENT_TIMESTAMP WHERE name = ? AND revoked_at IS NULL"), name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanToken(r rowScanner) (Token, error) {
	var t Token
	var zones string
	var lastUsed, revoked sql.NullTime
	err := r.Scan(&t.ID, &t.Name, &t.Scope, &t.OwnerRef, &zones, &t.CreatedAt, &lastUsed, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrNotFound
	}
	if err != nil {
		return Token{}, err
	}
	json.Unmarshal([]byte(zones), &t.Zones)
	if lastUsed.Valid {
		t.LastUsedAt = &lastUsed.Time
	}
	if revoked.Valid {
		t.RevokedAt = &revoked.Time
	}
	return t, nil
}

// Event is one audit-trail entry (events are append-only).
type Event struct {
	ID        int64     `json:"id"`
	Actor     string    `json:"actor,omitempty"`
	Action    string    `json:"action"`
	FromState string    `json:"from_state,omitempty"`
	ToState   string    `json:"to_state,omitempty"`
	Payload   string    `json:"payload,omitempty"`
	At        time.Time `json:"at"`
}

// ListEventsForSubject returns the audit trail for one subject, oldest first.
func (s *Store) ListEventsForSubject(ctx context.Context, subjectType string, subjectID int64) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, s.q(
		"SELECT id, actor, action, from_state, to_state, payload, at FROM events WHERE subject_type = ? AND subject_id = ? ORDER BY id"),
		subjectType, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.FromState, &e.ToState, &e.Payload, &e.At); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
