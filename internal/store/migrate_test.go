package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrationVersioning(t *testing.T) {
	p := filepath.Join(t.TempDir(), "v.db")
	s, err := Open("sqlite:" + p)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := s.schemaVersion(context.Background())
	if v != len(migrations) {
		t.Fatalf("version = %d want %d", v, len(migrations))
	}
	s.Close()
	// reopen: idempotent, stays at head
	s2, err := Open("sqlite:" + p)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if v2, _ := s2.schemaVersion(context.Background()); v2 != len(migrations) {
		t.Fatalf("reopen version = %d", v2)
	}
}
