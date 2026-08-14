package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/store"
)

func testSvc(t *testing.T) (*Alloc, *store.Store) {
	t.Helper()
	st, err := store.Open("sqlite:" + filepath.Join(t.TempDir(), "svc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ports := NewPorts(st)
	a := NewAlloc(st, ports)
	if _, err := st.EnsureZone(context.Background(), core.Zone{Name: "olsyn.com"}); err != nil {
		t.Fatal(err)
	}
	if err := ports.EnsureDefaultPool(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a, st
}

func TestAvailabilityReasons(t *testing.T) {
	a, _ := testSvc(t)
	ctx := context.Background()

	// blocked by policy
	av, err := a.CheckAvailability(ctx, "olsyn.com", "grafana")
	if err != nil || av.Available || av.Reason != "blocked" {
		t.Fatalf("grafana: %+v err=%v", av, err)
	}
	if len(av.Suggestions) == 0 {
		t.Error("blocked label should suggest alternatives")
	}

	// invalid
	av, _ = a.CheckAvailability(ctx, "olsyn.com", "-bad-")
	if av.Available || av.Reason != "invalid" {
		t.Fatalf("-bad-: %+v", av)
	}

	// free
	av, _ = a.CheckAvailability(ctx, "olsyn.com", "acme")
	if !av.Available {
		t.Fatalf("acme should be free: %+v", av)
	}

	// taken by tenant
	if _, err := a.Claim(ctx, ClaimRequest{Zone: "olsyn.com", Label: "acme", OwnerRef: "tenant-1"}); err != nil {
		t.Fatal(err)
	}
	av, _ = a.CheckAvailability(ctx, "olsyn.com", "ACME") // normalization folds case
	if av.Available || av.Reason != "taken" {
		t.Fatalf("ACME after claim: %+v", av)
	}

	// reserved by platform (platform claims bypass blocklist)
	if _, err := a.Claim(ctx, ClaimRequest{Zone: "olsyn.com", Label: "nucleus", Kind: core.KindPlatform, Source: core.SourceSeed}); err != nil {
		t.Fatal(err)
	}
	av, _ = a.CheckAvailability(ctx, "olsyn.com", "nucleus")
	if av.Available || av.Reason != "reserved" {
		t.Fatalf("nucleus: %+v", av)
	}
}

// Seed backfills grandfather existing tenants past the blocklist; the same
// label stays blocked for new signups (source=api).
func TestSeedBypassesPolicyButAPIDoesNot(t *testing.T) {
	a, _ := testSvc(t)
	ctx := context.Background()
	if _, err := a.Claim(ctx, ClaimRequest{Zone: "olsyn.com", Label: "test", Source: core.SourceSeed, OwnerRef: "legacy-tenant"}); err != nil {
		t.Fatalf("seed of blocklisted label should succeed: %v", err)
	}
	_, err := a.Claim(ctx, ClaimRequest{Zone: "olsyn.com", Label: "staging"})
	var rej *ErrClaimRejected
	if !errors.As(err, &rej) || rej.Reason != "blocked" {
		t.Fatalf("api claim of blocklisted label must stay blocked: %v", err)
	}
	// And uniqueness still applies to seeds.
	_, err = a.Claim(ctx, ClaimRequest{Zone: "olsyn.com", Label: "test", Source: core.SourceSeed})
	if !errors.As(err, &rej) || rej.Reason != "taken" {
		t.Fatalf("duplicate seed: want taken, got %v", err)
	}
}

func TestClaimRejectionCarriesReason(t *testing.T) {
	a, _ := testSvc(t)
	ctx := context.Background()
	_, err := a.Claim(ctx, ClaimRequest{Zone: "olsyn.com", Label: "admin"})
	var rej *ErrClaimRejected
	if !errors.As(err, &rej) || rej.Reason != "blocked" {
		t.Fatalf("want blocked rejection, got %v", err)
	}
}

func TestHoldLifecycle(t *testing.T) {
	a, st := testSvc(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return now }

	resp, err := a.Claim(ctx, ClaimRequest{Zone: "olsyn.com", Label: "newco", Hold: true})
	if err != nil {
		t.Fatal(err)
	}
	al := resp.Allocation
	if al.State != core.StatePending || al.ExpiresAt == nil {
		t.Fatalf("hold not pending+expiring: %+v", al)
	}
	if got := al.ExpiresAt.Sub(now); got != 15*time.Minute {
		t.Fatalf("default TTL: got %v", got)
	}

	// while held, availability says taken
	av, _ := a.CheckAvailability(ctx, "olsyn.com", "newco")
	if av.Available {
		t.Fatal("held label reported available")
	}

	// expiry reaps it
	if n, _ := st.ReapExpiredHolds(ctx, now.Add(16*time.Minute)); n != 1 {
		t.Fatalf("reap should remove 1, got %d", n)
	}
	av, _ = a.CheckAvailability(ctx, "olsyn.com", "newco")
	if !av.Available {
		t.Fatalf("after reap should be free: %+v", av)
	}

	// hold → commit clears expiry
	resp, _ = a.Claim(ctx, ClaimRequest{Zone: "olsyn.com", Label: "newco", Hold: true})
	committed, err := a.Commit(ctx, resp.Allocation.ID)
	if err != nil || committed.State != core.StateActive || committed.ExpiresAt != nil {
		t.Fatalf("commit: %+v err=%v", committed, err)
	}
	// committed allocations survive the reaper
	if n, _ := st.ReapExpiredHolds(ctx, now.Add(24*time.Hour)); n != 0 {
		t.Fatalf("reaper ate a committed allocation (n=%d)", n)
	}
}

func TestRename(t *testing.T) {
	a, _ := testSvc(t)
	ctx := context.Background()
	resp, err := a.Claim(ctx, ClaimRequest{
		Zone: "olsyn.com", Label: "oldname", OwnerRef: "tenant-9",
		Spec: core.Spec{Wildcard: true, Routes: []core.Route{{Backend: core.Backend{Kind: "address", Address: &core.AddressBackend{Host: "app", Port: 80}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := resp.Allocation.ID
	a.Claim(ctx, ClaimRequest{Zone: "olsyn.com", Label: "occupied", OwnerRef: "tenant-x"})

	// success keeps identity, spec, owner
	renamed, err := a.Rename(ctx, id, "NewName") // normalization folds case
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID != id || renamed.Label != "newname" || renamed.FQDN != "newname.olsyn.com" {
		t.Fatalf("rename result: %+v", renamed)
	}
	if renamed.OwnerRef != "tenant-9" || !renamed.Spec.Wildcard || len(renamed.Spec.Routes) != 1 {
		t.Fatalf("identity lost: %+v", renamed)
	}
	// old label is free again, atomically
	if av, _ := a.CheckAvailability(ctx, "olsyn.com", "oldname"); !av.Available {
		t.Fatalf("old label not freed: %+v", av)
	}

	// conflict → taken with suggestions, allocation untouched
	_, err = a.Rename(ctx, id, "occupied")
	var rej *ErrClaimRejected
	if !errors.As(err, &rej) || rej.Reason != "taken" {
		t.Fatalf("rename onto taken: %v", err)
	}
	still, _ := a.Store.GetAllocation(ctx, id)
	if still.Label != "newname" {
		t.Fatalf("failed rename mutated the row: %+v", still)
	}

	// policy still guards tenant renames
	if _, err = a.Rename(ctx, id, "grafana"); !errors.As(err, &rej) || rej.Reason != "blocked" {
		t.Fatalf("rename onto blocked: %v", err)
	}
	// invalid label
	if _, err = a.Rename(ctx, id, "-bad-"); !errors.As(err, &rej) || rej.Reason != "invalid" {
		t.Fatalf("rename invalid: %v", err)
	}
	// no-op rename succeeds
	if _, err = a.Rename(ctx, id, "newname"); err != nil {
		t.Fatalf("no-op rename: %v", err)
	}
}

func TestReleaseFreesLabel(t *testing.T) {
	a, _ := testSvc(t)
	ctx := context.Background()
	resp, err := a.Claim(ctx, ClaimRequest{Zone: "olsyn.com", Label: "shortlived"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Release(ctx, resp.Allocation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Claim(ctx, ClaimRequest{Zone: "olsyn.com", Label: "shortlived"}); err != nil {
		t.Fatalf("re-claim after release: %v", err)
	}
}

func TestPortBindTestSkipsOccupied(t *testing.T) {
	_, st := testSvc(t)
	ctx := context.Background()
	ports := NewPorts(st)
	pool, err := st.EnsurePool(ctx, core.PortPool{Name: "bindtest", RangeStart: 52000, RangeEnd: 52010})
	if err != nil {
		t.Fatal(err)
	}
	_ = pool
	// Physically occupy the first candidate.
	l, err := net.Listen("tcp", "127.0.0.1:52000")
	if err != nil {
		t.Skip("cannot bind 52000 on this machine")
	}
	defer l.Close()
	pa, err := ports.Claim(ctx, "bindtest", "", "proj-x")
	if err != nil {
		t.Fatal(err)
	}
	if pa.Value == 52000 {
		t.Fatal("granted an occupied port")
	}
	// Sticky: second claim returns the same value.
	pa2, err := ports.Claim(ctx, "bindtest", "", "proj-x")
	if err != nil || pa2.Value != pa.Value {
		t.Fatalf("stickiness: %d vs %d err=%v", pa.Value, pa2.Value, err)
	}
}

func TestPortAvoidList(t *testing.T) {
	_, st := testSvc(t)
	ctx := context.Background()
	ports := NewPorts(st)
	ports.SkipBindTest = true
	if _, err := st.EnsurePool(ctx, core.PortPool{Name: "avoid", RangeStart: 8079, RangeEnd: 8082, Avoid: []int{8080}}); err != nil {
		t.Fatal(err)
	}
	got := map[int]bool{}
	for i := 0; i < 3; i++ {
		pa, err := ports.Claim(ctx, "avoid", "", fmt.Sprintf("p%d", i))
		if err != nil {
			t.Fatal(err)
		}
		got[pa.Value] = true
	}
	if got[8080] {
		t.Fatal("avoid list ignored")
	}
}

func TestCombinedClaim(t *testing.T) {
	a, _ := testSvc(t)
	a.Ports.SkipBindTest = true
	resp, err := a.Claim(context.Background(), ClaimRequest{
		Zone: "olsyn.com", Label: "combo", OwnerRef: "combo-proj", PortPool: "dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Allocation == nil || resp.Port == nil {
		t.Fatalf("want hostname+port, got %+v", resp)
	}
	if resp.Port.Value < DefaultPoolStart {
		t.Fatalf("port below pool start: %d", resp.Port.Value)
	}
}
