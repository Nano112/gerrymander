package store

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open("sqlite:" + filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The invariant test: N goroutines race one label; exactly one wins.
func TestAllocationRace(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	z, err := s.EnsureZone(ctx, core.Zone{Name: "race.test"})
	if err != nil {
		t.Fatal(err)
	}
	const n = 32
	var wins, losses atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.CreateAllocation(ctx, core.Allocation{
				ZoneID: z.ID, Label: "contested", FQDN: "contested.race.test",
				Kind: core.KindTenant, Source: core.SourceAPI, State: core.StateActive,
			})
			switch err {
			case nil:
				wins.Add(1)
			case ErrTaken:
				losses.Add(1)
			default:
				t.Errorf("goroutine %d: unexpected error %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 || losses.Load() != n-1 {
		t.Fatalf("want exactly 1 win, %d losses; got %d wins, %d losses", n-1, wins.Load(), losses.Load())
	}
}

// Port race: no value issued twice; stickiness returns the same value.
func TestPortRaceAndStickiness(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	pool, err := s.EnsurePool(ctx, core.PortPool{Name: "dev", RangeStart: 51000, RangeEnd: 51100})
	if err != nil {
		t.Fatal(err)
	}
	const n = 24
	var wg sync.WaitGroup
	values := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			owner := "proj-" + string(rune('a'+i))
			// naive candidate walk mimicking the service layer's loop
			for v := pool.RangeStart; v <= pool.RangeEnd; v++ {
				pa, err := s.InsertPort(ctx, pool.ID, "", owner, v)
				if err == ErrTaken {
					continue
				}
				if err != nil {
					t.Errorf("owner %s: %v", owner, err)
					return
				}
				values[i] = pa.Value
				return
			}
			t.Errorf("owner %s: pool exhausted", owner)
		}(i)
	}
	wg.Wait()
	seen := map[int]bool{}
	for _, v := range values {
		if v == 0 {
			t.Fatal("unallocated slot")
		}
		if seen[v] {
			t.Fatalf("port %d issued twice", v)
		}
		seen[v] = true
	}
	// Stickiness: same owner cannot get a second value in the pool.
	if _, err := s.InsertPort(ctx, pool.ID, "", "proj-a", 51099); err != ErrTaken {
		t.Fatalf("second value for same owner: want ErrTaken, got %v", err)
	}
	pa, err := s.GetPortByOwner(ctx, pool.ID, "proj-a")
	if err != nil || pa.Value == 0 {
		t.Fatalf("sticky lookup: %v %+v", err, pa)
	}
}

func TestHoldReap(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	z, _ := s.EnsureZone(ctx, core.Zone{Name: "hold.test"})
	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Hour)
	mk := func(label string, exp *time.Time) {
		if _, err := s.CreateAllocation(ctx, core.Allocation{ZoneID: z.ID, Label: label, FQDN: label + ".hold.test", Kind: core.KindTenant, Source: core.SourceAPI, State: core.StatePending, ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
	}
	mk("expired", &past)
	mk("fresh", &future)
	mk("nohold", nil)
	n, err := s.ReapExpiredHolds(ctx, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("reap: n=%d err=%v", n, err)
	}
	if _, err := s.GetAllocationByLabel(ctx, "hold.test", "expired"); err != ErrNotFound {
		t.Fatalf("expired hold should be gone, got %v", err)
	}
	if _, err := s.GetAllocationByLabel(ctx, "hold.test", "fresh"); err != nil {
		t.Fatalf("fresh hold should remain: %v", err)
	}
}

func TestSpecStatusRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	z, _ := s.EnsureZone(ctx, core.Zone{Name: "rt.test"})
	a := core.Allocation{
		ZoneID: z.ID, Label: "app", FQDN: "app.rt.test", Kind: core.KindPlatform, Source: core.SourceSeed, State: core.StateActive,
		Spec: core.Spec{Wildcard: true, Routes: []core.Route{{Listen: 0, Backend: core.Backend{Kind: "address", Address: &core.AddressBackend{Host: "olsyn-app", Port: 80, PreserveHost: true}}}, {Listen: 5175, Backend: core.Backend{Kind: "address", Address: &core.AddressBackend{Host: "olsyn-app", Port: 5175}}}}},
		Labels: map[string]string{"note": "caddy-parity"},
	}
	created, err := s.CreateAllocation(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAllocation(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Spec.Wildcard || len(got.Spec.Routes) != 2 || got.Spec.Routes[1].Listen != 5175 {
		t.Fatalf("spec did not round-trip: %+v", got.Spec)
	}
	if got.Spec.Routes[0].Backend.Address.Host != "olsyn-app" || !got.Spec.Routes[0].Backend.Address.PreserveHost {
		t.Fatalf("backend did not round-trip: %+v", got.Spec.Routes[0].Backend)
	}
	got.Status.SetCondition(core.ConditionStatus{Type: core.CondReady, Status: true, At: time.Now()})
	got.State = core.StateActive
	if err := s.UpdateAllocation(ctx, got); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.GetAllocation(ctx, created.ID)
	if len(got2.Status.Conditions) != 1 || got2.Status.Conditions[0].Type != core.CondReady {
		t.Fatalf("status did not round-trip: %+v", got2.Status)
	}
}

func TestIdempotency(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.RememberIdempotent(ctx, "k1", `{"id":1}`); err != nil {
		t.Fatal(err)
	}
	// replay stores nothing new
	if err := s.RememberIdempotent(ctx, "k1", `{"id":2}`); err != nil {
		t.Fatal(err)
	}
	resp, ok, err := s.RecallIdempotent(ctx, "k1")
	if err != nil || !ok || resp != `{"id":1}` {
		t.Fatalf("recall: %q %v %v", resp, ok, err)
	}
}
