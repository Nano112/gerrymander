package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nano112/gerrymander/catalog"
	"github.com/Nano112/gerrymander/internal/core"
)

// Optional PostgreSQL coverage uses a fresh schema per test, never existing data.
func catalogTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("GERRY_TEST_POSTGRES_DSN")
	if dsn == "" {
		return testStore(t)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("catalog_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec("DROP SCHEMA " + schema + " CASCADE"); admin.Close() })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, err := Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func catalogFixture() catalog.Bundle {
	return catalog.Bundle{
		SchemaVersion: 1,
		Application:   catalog.Application{ID: "app:test", Project: "test", Display: catalog.Display{Name: "Test"}},
		Services:      []catalog.Service{{ID: "api", ApplicationID: "app:test", Key: "api", Display: catalog.Display{Name: "API"}}},
		Capabilities:  []catalog.Capabilities{{ServiceID: "api", Version: "v1", EntryPermission: "access", Permissions: map[string]string{"access": "Open"}, Roles: map[string][]string{"viewer": {"access"}}}},
		Deployments:   []catalog.Deployment{{ID: "deploy:prod", ApplicationID: "app:test", ServiceID: "api", Environment: "production", CapabilityVersion: "v1", Lifecycle: "registered", LaunchURL: "https://example.com"}},
	}
}

func TestCatalogVersionsAndLifecycle(t *testing.T) {
	s := catalogTestStore(t)
	ctx := t.Context()
	b := catalogFixture()
	if err := s.CheckCatalog(ctx, b); err != nil {
		t.Fatal(err)
	}
	empty, _ := s.Catalog(ctx, "")
	if len(empty.Applications) != 0 {
		t.Fatal("preflight persisted data")
	}
	for i := 0; i < 2; i++ {
		if err := s.ApplyCatalog(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	b.Capabilities[0].Permissions["admin"] = "Admin"
	b.Application.Display.Name = "Should roll back"
	if err := s.ApplyCatalog(ctx, b); !errors.Is(err, ErrCatalogConflict) {
		t.Fatalf("mutable version: %v", err)
	}
	snap, _ := s.Catalog(ctx, "")
	if snap.Applications[0].Display.Name != "Test" {
		t.Fatal("failed bundle partially applied")
	}
	b.Capabilities[0].Version = "v2"
	b.Application.Display.Name = "Renamed"
	if err := s.ApplyCatalog(ctx, b); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Catalog(ctx, "")
	entries := snap.Access("production", "")
	if len(entries) != 1 || entries[0].Capabilities.Version != "v1" {
		t.Fatal("publishing v2 silently activated it")
	}
	if len(snap.Access("development", "")) != 0 || len(snap.Access("production", "alice")) != 0 {
		t.Fatal("environment/workspace leak")
	}
	b.Deployments[0].CapabilityVersion = "v2"
	if err := s.ApplyCatalog(ctx, b); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Catalog(ctx, "")
	if snap.Access("production", "")[0].Capabilities.Version != "v2" {
		t.Fatal("explicit activation missing")
	}
	if err := s.DeactivateCatalogDeployment(ctx, "deploy:prod"); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Catalog(ctx, "")
	if len(snap.Access("production", "")) != 0 || len(snap.Services) != 1 {
		t.Fatal("retirement must retain identity but remove access discovery")
	}
}

func TestCatalogIdentityAndReferences(t *testing.T) {
	s := catalogTestStore(t)
	ctx := t.Context()
	b := catalogFixture()
	if err := s.ApplyCatalog(ctx, b); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*catalog.Bundle){
		func(b *catalog.Bundle) { b.Services[0].Key = "renamed" },
		func(b *catalog.Bundle) { b.Deployments[0].Environment = "staging" },
		func(b *catalog.Bundle) { b.Application.Project = "other" },
	} {
		next := catalogFixture()
		mutate(&next)
		if err := s.ApplyCatalog(ctx, next); !errors.Is(err, ErrCatalogConflict) {
			t.Fatalf("identity rewrite accepted: %v", err)
		}
	}
	b.Deployments[0].CapabilityVersion = "missing"
	if err := s.ApplyCatalog(ctx, b); err == nil {
		t.Fatal("unknown version accepted")
	}
	b = catalogFixture()
	b.Application.Display.Logo = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := s.ApplyCatalog(ctx, b); err == nil {
		t.Fatal("unknown asset accepted")
	}
	a, err := catalog.NewAsset([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><path d="M0 0L10 10"/></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutCatalogAsset(ctx, a); err != nil {
		t.Fatal(err)
	}
	b.Application.Display.Logo = a.ID
	if err := s.ApplyCatalog(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, err := s.CatalogAsset(ctx, a.ID)
	if err != nil || string(got.Data) != string(a.Data) {
		t.Fatalf("asset roundtrip: %v", err)
	}
}

func TestCatalogConcurrentImmutableVersion(t *testing.T) {
	s := catalogTestStore(t)
	var wg sync.WaitGroup
	var wins, conflicts atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := catalogFixture()
			b.Capabilities[0].Permissions["access"] = string(rune('A' + i))
			err := s.ApplyCatalog(context.Background(), b)
			if err == nil {
				wins.Add(1)
			} else if errors.Is(err, ErrCatalogConflict) {
				conflicts.Add(1)
			} else {
				t.Errorf("unexpected: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 || conflicts.Load() != 11 {
		t.Fatalf("wins=%d conflicts=%d", wins.Load(), conflicts.Load())
	}
}

func TestCatalogUpgradePreservesExistingRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range migrations[:2] {
		for _, stmt := range strings.Split(ddl, ";") {
			if strings.TrimSpace(stmt) != "" {
				if _, err := db.Exec(stmt); err != nil {
					db.Close()
					t.Fatal(err)
				}
			}
		}
	}
	old := &Store{db: db}
	ctx := t.Context()
	if _, err := old.EnsureZone(ctx, core.Zone{Name: "preserved.test"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := old.CreateToken(ctx, "existing", "admin", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open("sqlite:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	zones, err := s.ListZones(ctx)
	if err != nil || len(zones) != 1 || zones[0].Name != "preserved.test" {
		t.Fatalf("zones lost: %v %v", zones, err)
	}
	tokens, err := s.ListTokens(ctx)
	if err != nil || len(tokens) != 1 || tokens[0].Name != "existing" {
		t.Fatalf("tokens lost: %v %v", tokens, err)
	}
	if err := s.ApplyCatalog(ctx, catalogFixture()); err != nil {
		t.Fatal(err)
	}
}
