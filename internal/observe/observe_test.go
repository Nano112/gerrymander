package observe

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/store"
)

// Representative exact, wildcard and path-qualified ingress matches.
func TestParseMatchRealShapes(t *testing.T) {
	cases := []struct {
		match     string
		exact     []string
		catchAlls []string
	}{
		{"Host(`grafana.example.com`)", []string{"grafana.example.com"}, nil},
		{"Host(`example.com`) || Host(`www.example.com`)", []string{"example.com", "www.example.com"}, nil},
		{"Host(`inventory.example.com`) && PathPrefix(`/v1/api`)", []string{"inventory.example.com"}, nil},
		{"HostRegexp(`^[a-z0-9-]+\\.example\\.com$`)", nil, []string{"example.com"}},
		{"HostRegexp(`^[a-z0-9-]+\\.staging\\.example\\.com$`)", nil, []string{"staging.example.com"}},
		{"Host(`app.example.com`) || HostRegexp(`^[a-z0-9-]+\\.app\\.example\\.com$`)", []string{"app.example.com"}, []string{"app.example.com"}},
		{"Host(`farm.example.com`) && Path(`/`)", []string{"farm.example.com"}, nil},
		{"Host(`rt.staging.example.com`) || Host(`rt.example.com`)", []string{"rt.staging.example.com", "rt.example.com"}, nil},
		{"Host(`preview.app.example.com`) || HostRegexp(`^[a-z0-9-]+\\.preview\\.app\\.example\\.com$`)", []string{"preview.app.example.com"}, []string{"preview.app.example.com"}},
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

// Exercises routes with equal priorities: catch-all at priority 1, a bare
// Host route also at priority 1 → shadowed. Same route at priority 10 → fine.
func TestShadowCheckReproducesTheTrap(t *testing.T) {
	catchAll := ObservedRoute{
		Namespace: "example-production", Name: "example-app-tenant-domains",
		Match: "HostRegexp(`^[a-z0-9-]+\\.example\\.com$`)", Priority: 1,
	}
	catchAll.Hosts = ParseMatch(catchAll.Match)

	shadowed := ObservedRoute{
		Namespace: "inventory", Name: "inventory",
		Match: "Host(`inventory.example.com`)", Priority: 1, // the original bug
	}
	shadowed.Hosts = ParseMatch(shadowed.Match)

	safe := ObservedRoute{
		Namespace: "monitoring", Name: "grafana",
		Match: "Host(`grafana.example.com`)", // default priority = len(match) >> 1
	}
	safe.Hosts = ParseMatch(safe.Match)

	deep := ObservedRoute{
		Namespace: "telemetry", Name: "rtd",
		Match: "Host(`rt.staging.example.com`)", Priority: 1, // multi-label: outside the class
	}
	deep.Hosts = ParseMatch(deep.Match)

	conflicts := ShadowCheck("example.com", []ObservedRoute{catchAll, shadowed, safe, deep})
	if len(conflicts) != 1 {
		t.Fatalf("want exactly the inventory conflict, got %+v", conflicts)
	}
	c := conflicts[0]
	if c.Label != "inventory" || c.Type != "shadowed-host" || c.Route != "inventory/inventory" {
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
	z, _ := st.EnsureZone(ctx, core.Zone{Name: "example.com"})

	// Pre-existing tenant that a cluster route will collide with.
	st.CreateAllocation(ctx, core.Allocation{
		ZoneID: z.ID, Label: "demo", FQDN: "demo.example.com", Kind: core.KindTenant,
		Source: core.SourceSeed, State: core.StateActive, OwnerRef: "tenant-uuid-demo",
	})

	mk := func(ns, name, match string, prio int) ObservedRoute {
		r := ObservedRoute{Namespace: ns, Name: name, Kind: "IngressRoute", Match: match, Priority: prio}
		r.Hosts = ParseMatch(match)
		return r
	}
	routes := []ObservedRoute{
		mk("monitoring", "grafana", "Host(`grafana.example.com`)", 0),
		mk("inventory", "inventory", "Host(`inventory.example.com`)", 10),
		mk("rogue", "hijack", "Host(`demo.example.com`)", 0), // collides with tenant
		mk("example-production", "example-app-tenant-domains", "HostRegexp(`^[a-z0-9-]+\\.example\\.com$`)", 1),
		mk("example-preview", "example-app", "Host(`preview.app.example.com`) || HostRegexp(`^[a-z0-9-]+\\.preview\\.app\\.example\\.com$`)", 0),
		mk("outside", "other", "Host(`something.example.org`)", 0),
	}

	obs := &Observer{Store: st, Zones: []string{"example.com"}, AutoRegister: true}
	if err := obs.Reconcile(ctx, routes); err != nil {
		t.Fatal(err)
	}

	// grafana imported as platform/observed
	a, err := st.GetAllocationByLabel(ctx, "example.com", "grafana")
	if err != nil || a.Kind != core.KindPlatform || a.Source != core.SourceObserved {
		t.Fatalf("grafana import: %+v err=%v", a, err)
	}
	// wildcard label from the cowboy regexp
	if _, err := st.GetAllocationByLabel(ctx, "example.com", "*.preview.app"); err != nil {
		t.Fatalf("wildcard import: %v", err)
	}
	// out-of-zone host ignored
	if _, err := st.GetAllocationByLabel(ctx, "example.com", "something"); err == nil {
		t.Fatal("out-of-zone host imported")
	}
	// tenant collision reported, tenant NOT overwritten
	a, _ = st.GetAllocationByLabel(ctx, "example.com", "demo")
	if a.Kind != core.KindTenant || a.OwnerRef != "tenant-uuid-demo" {
		t.Fatalf("tenant was mutated by observer: %+v", a)
	}
	conflicts := obs.Conflicts()
	foundKindMismatch := false
	for _, c := range conflicts {
		if c["type"] == "kind-mismatch" && c["label"] == "demo" {
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
	z, _ := st.EnsureZone(ctx, core.Zone{Name: "example.com"})
	st.CreateAllocation(ctx, core.Allocation{
		ZoneID: z.ID, Label: "demo", FQDN: "demo.example.com", Kind: core.KindTenant,
		Source: core.SourceSeed, State: core.StateActive, OwnerRef: "tenant-uuid-demo",
	})

	obs := &Observer{Store: st, Zones: []string{"example.com"}}
	routes := []ObservedRoute{{
		Namespace: "example-production", Name: "gerry-example-com-demo", Kind: "IngressRoute",
		Match: "Host(`demo.example.com`)", Priority: 10,
		Hosts:   ParseMatch("Host(`demo.example.com`)"),
		Managed: true, // the actuator materialized this tenant's allocation
	}}
	if err := obs.Reconcile(ctx, routes); err != nil {
		t.Fatal(err)
	}
	for _, c := range obs.Conflicts() {
		if c["label"] == "demo" {
			t.Fatalf("actuator's own route classified as conflict: %+v", c)
		}
	}
}
