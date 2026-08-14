package observe

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/store"
)

// The nine real match shapes captured from the olsyn-edge cluster.
func TestParseMatchRealShapes(t *testing.T) {
	cases := []struct {
		match     string
		exact     []string
		catchAlls []string
	}{
		{"Host(`grafana.olsyn.com`)", []string{"grafana.olsyn.com"}, nil},
		{"Host(`olsyn.com`) || Host(`www.olsyn.com`)", []string{"olsyn.com", "www.olsyn.com"}, nil},
		{"Host(`nucleus.olsyn.com`) && PathPrefix(`/omni/api`)", []string{"nucleus.olsyn.com"}, nil},
		{"HostRegexp(`^[a-z0-9-]+\\.olsyn\\.com$`)", nil, []string{"olsyn.com"}},
		{"HostRegexp(`^[a-z0-9-]+\\.staging\\.olsyn\\.com$`)", nil, []string{"staging.olsyn.com"}},
		{"Host(`app.olsyn.com`) || HostRegexp(`^[a-z0-9-]+\\.app\\.olsyn\\.com$`)", []string{"app.olsyn.com"}, []string{"app.olsyn.com"}},
		{"Host(`farm.olsyn.com`) && Path(`/`)", []string{"farm.olsyn.com"}, nil},
		{"Host(`rt.staging.olsyn.com`) || Host(`rt.olsyn.com`)", []string{"rt.staging.olsyn.com", "rt.olsyn.com"}, nil},
		{"Host(`cb.app.olsyn.com`) || HostRegexp(`^[a-z0-9-]+\\.cb\\.app\\.olsyn\\.com$`)", []string{"cb.app.olsyn.com"}, []string{"cb.app.olsyn.com"}},
	}
	for _, c := range cases {
		got := ParseMatch(c.match)
		var exact, ca []string
		for _, h := range got {
			if h.Exact {
				exact = append(exact, h.Host)
			}
			if h.CatchAll {
				ca = append(ca, h.Suffix)
			}
		}
		if !eq(exact, c.exact) || !eq(ca, c.catchAlls) {
			t.Errorf("ParseMatch(%q): exact=%v catchAll=%v, want %v / %v", c.match, exact, ca, c.exact, c.catchAlls)
		}
	}
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Reproduces the olsyn.com priority-1 trap: catch-all at priority 1, a bare
// Host route also at priority 1 → shadowed. Same route at priority 10 → fine.
func TestShadowCheckReproducesTheTrap(t *testing.T) {
	catchAll := ObservedRoute{
		Namespace: "olsyn-production", Name: "olsyn-app-tenant-domains",
		Match: "HostRegexp(`^[a-z0-9-]+\\.olsyn\\.com$`)", Priority: 1,
	}
	catchAll.Hosts = ParseMatch(catchAll.Match)

	shadowed := ObservedRoute{
		Namespace: "nucleus", Name: "nucleus",
		Match: "Host(`nucleus.olsyn.com`)", Priority: 1, // the original bug
	}
	shadowed.Hosts = ParseMatch(shadowed.Match)

	safe := ObservedRoute{
		Namespace: "monitoring", Name: "grafana",
		Match: "Host(`grafana.olsyn.com`)", // default priority = len(match) >> 1
	}
	safe.Hosts = ParseMatch(safe.Match)

	deep := ObservedRoute{
		Namespace: "telemetry", Name: "rtd",
		Match: "Host(`rt.staging.olsyn.com`)", Priority: 1, // multi-label: outside the class
	}
	deep.Hosts = ParseMatch(deep.Match)

	conflicts := ShadowCheck("olsyn.com", []ObservedRoute{catchAll, shadowed, safe, deep})
	if len(conflicts) != 1 {
		t.Fatalf("want exactly the nucleus conflict, got %+v", conflicts)
	}
	c := conflicts[0]
	if c.Label != "nucleus" || c.Type != "shadowed-host" || c.Route != "nucleus/nucleus" {
		t.Fatalf("wrong conflict: %+v", c)
	}
}

func TestReconcileImportsAndConflicts(t *testing.T) {
	st, err := store.Open("sqlite:" + filepath.Join(t.TempDir(), "obs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	z, _ := st.EnsureZone(ctx, core.Zone{Name: "olsyn.com"})

	// Pre-existing tenant that a cluster route will collide with.
	st.CreateAllocation(ctx, core.Allocation{
		ZoneID: z.ID, Label: "gv", FQDN: "gv.olsyn.com", Kind: core.KindTenant,
		Source: core.SourceSeed, State: core.StateActive, OwnerRef: "tenant-uuid-gv",
	})

	mk := func(ns, name, match string, prio int) ObservedRoute {
		r := ObservedRoute{Namespace: ns, Name: name, Kind: "IngressRoute", Match: match, Priority: prio}
		r.Hosts = ParseMatch(match)
		return r
	}
	routes := []ObservedRoute{
		mk("monitoring", "grafana", "Host(`grafana.olsyn.com`)", 0),
		mk("nucleus", "nucleus", "Host(`nucleus.olsyn.com`)", 10),
		mk("rogue", "hijack", "Host(`gv.olsyn.com`)", 0), // collides with tenant
		mk("olsyn-production", "olsyn-app-tenant-domains", "HostRegexp(`^[a-z0-9-]+\\.olsyn\\.com$`)", 1),
		mk("olsyn-cowboy", "olsyn-app", "Host(`cb.app.olsyn.com`) || HostRegexp(`^[a-z0-9-]+\\.cb\\.app\\.olsyn\\.com$`)", 0),
		mk("outside", "other", "Host(`something.example.org`)", 0),
	}

	obs := &Observer{Store: st, Zones: []string{"olsyn.com"}, AutoRegister: true}
	if err := obs.Reconcile(ctx, routes); err != nil {
		t.Fatal(err)
	}

	// grafana imported as platform/observed
	a, err := st.GetAllocationByLabel(ctx, "olsyn.com", "grafana")
	if err != nil || a.Kind != core.KindPlatform || a.Source != core.SourceObserved {
		t.Fatalf("grafana import: %+v err=%v", a, err)
	}
	// wildcard label from the cowboy regexp
	if _, err := st.GetAllocationByLabel(ctx, "olsyn.com", "*.cb.app"); err != nil {
		t.Fatalf("wildcard import: %v", err)
	}
	// out-of-zone host ignored
	if _, err := st.GetAllocationByLabel(ctx, "olsyn.com", "something"); err == nil {
		t.Fatal("out-of-zone host imported")
	}
	// tenant collision reported, tenant NOT overwritten
	a, _ = st.GetAllocationByLabel(ctx, "olsyn.com", "gv")
	if a.Kind != core.KindTenant || a.OwnerRef != "tenant-uuid-gv" {
		t.Fatalf("tenant was mutated by observer: %+v", a)
	}
	conflicts := obs.Conflicts()
	foundKindMismatch := false
	for _, c := range conflicts {
		if c["type"] == "kind-mismatch" && c["label"] == "gv" {
			foundKindMismatch = true
		}
	}
	if !foundKindMismatch {
		t.Fatalf("kind-mismatch not reported: %+v", conflicts)
	}
	// Re-reconcile is idempotent (no dup errors, same conflicts).
	if err := obs.Reconcile(ctx, routes); err != nil {
		t.Fatal(err)
	}
}

func TestManagedRoutesAreNeverClassified(t *testing.T) {
	st, err := store.Open("sqlite:" + filepath.Join(t.TempDir(), "managed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	z, _ := st.EnsureZone(ctx, core.Zone{Name: "olsyn.com"})
	st.CreateAllocation(ctx, core.Allocation{
		ZoneID: z.ID, Label: "gv", FQDN: "gv.olsyn.com", Kind: core.KindTenant,
		Source: core.SourceSeed, State: core.StateActive, OwnerRef: "tenant-uuid-gv",
	})

	obs := &Observer{Store: st, Zones: []string{"olsyn.com"}}
	routes := []ObservedRoute{{
		Namespace: "olsyn-production", Name: "gerry-olsyn-com-gv", Kind: "IngressRoute",
		Match: "Host(`gv.olsyn.com`)", Priority: 10,
		Hosts:   ParseMatch("Host(`gv.olsyn.com`)"),
		Managed: true, // the actuator materialized this tenant's allocation
	}}
	if err := obs.Reconcile(ctx, routes); err != nil {
		t.Fatal(err)
	}
	for _, c := range obs.Conflicts() {
		if c["label"] == "gv" {
			t.Fatalf("actuator's own route classified as conflict: %+v", c)
		}
	}
}
