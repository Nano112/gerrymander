package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
)

func TestObservationRetirementAuditAndMinimumScans(t *testing.T) {
	// Also exercised against PostgreSQL in CI via the isolated-schema helper.
	s := catalogTestStore(t)
	ctx := t.Context()
	z, err := s.EnsureZone(ctx, core.Zone{Name: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAllocation(ctx, core.Allocation{ZoneID: z.ID, Label: "gone", FQDN: "gone.example.com", Kind: core.KindPlatform, Source: core.SourceObserved, State: core.StateActive, OwnerRef: "sessions/old"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	reconcile := func(at time.Time, reset bool) ObservationResult {
		t.Helper()
		result, err := s.ReconcileObservations(ctx, z.Name, nil, at, time.Minute, true, reset)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	reconcile(now, true)
	if result := reconcile(now.Add(time.Minute), false); result.Retired != 0 {
		t.Fatal("retired before three complete missing scans")
	}
	if result := reconcile(now.Add(2*time.Minute), false); result.Retired != 1 {
		t.Fatalf("not retired: %+v", result)
	}
	if _, err := s.GetAllocationByLabel(ctx, z.Name, "gone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("label still reserved: %v", err)
	}
	var payload string
	if err := s.db.QueryRowContext(ctx, "SELECT payload FROM events WHERE action='retire-missing'").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var archived core.Allocation
	if err := json.Unmarshal([]byte(payload), &archived); err != nil {
		t.Fatal(err)
	}
	if archived.Label != "gone" || archived.ZoneName != z.Name || archived.OwnerRef != "sessions/old" || archived.Status.Observation.MissingObservations != 3 {
		t.Fatalf("incomplete audit: %s", payload)
	}
	// A new claimant can reuse the label; no released row blocks rediscovery.
	if _, err := s.CreateAllocation(ctx, core.Allocation{ZoneID: z.ID, Label: "gone", FQDN: "gone.example.com", Kind: core.KindTenant, Source: core.SourceAPI, State: core.StateActive}); err != nil {
		t.Fatal(err)
	}
	reconcile(now.Add(3*time.Minute), false)
	a, err := s.GetAllocationByLabel(ctx, z.Name, "gone")
	if err != nil || a.Source != core.SourceAPI {
		t.Fatalf("new claim was lost: %+v %v", a, err)
	}
}

func TestObservationRetirementRollsBackWhenAuditFails(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	z, err := s.EnsureZone(ctx, core.Zone{Name: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAllocation(ctx, core.Allocation{ZoneID: z.ID, Label: "gone", FQDN: "gone.example.com", Kind: core.KindPlatform, Source: core.SourceObserved, State: core.StateActive}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		if _, err := s.ReconcileObservations(ctx, z.Name, nil, now.Add(time.Duration(i)*time.Minute), time.Minute, true, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER reject_retirement BEFORE INSERT ON events WHEN NEW.action='retire-missing' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileObservations(ctx, z.Name, nil, now.Add(2*time.Minute), time.Minute, true, false); err == nil {
		t.Fatal("expected audit failure")
	}
	a, err := s.GetAllocationByLabel(ctx, z.Name, "gone")
	if err != nil {
		t.Fatalf("retired without audit: %v", err)
	}
	if a.Status.Observation.MissingObservations != 2 {
		t.Fatal("failed transaction changed lifecycle")
	}
}
