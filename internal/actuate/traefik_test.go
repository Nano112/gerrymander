package actuate

import (
	"strings"
	"testing"

	"github.com/Nano112/gerrymander/internal/core"
)

func TestRouteNameDeterministicAndSafe(t *testing.T) {
	if RouteName("olsyn.com", "gv") != RouteName("olsyn.com", "gv") {
		t.Fatal("not deterministic")
	}
	if RouteName("olsyn.com", "@") != "gerry-olsyn-com-apex" {
		t.Fatalf("apex: %s", RouteName("olsyn.com", "@"))
	}
	n := RouteName(strings.Repeat("verylongzone.", 6)+"com", "some-label")
	if len(n) > 63 {
		t.Fatalf("name exceeds k8s limit: %d", len(n))
	}
	if RouteName("z.com", "*.cb.app") != "gerry-z-com-cb-app" {
		t.Fatalf("wildcard label: %s", RouteName("z.com", "*.cb.app"))
	}
}

func TestDesiredEnforcesMinPriorityAndLabel(t *testing.T) {
	a := &Actuator{}
	al := core.Allocation{
		ZoneName: "example.com", Label: "shop", FQDN: "shop.example.com",
		State: core.StateActive, OwnerRef: "tenant-9",
		Spec: core.Spec{
			Priority: 1, // below the floor — must be raised
			Routes: []core.Route{{Backend: core.Backend{Kind: "service",
				Service: &core.ServiceBackend{Namespace: "shop-ns", Name: "shop-svc", Port: 8080}}}},
		},
	}
	ns, ir, ok := a.desired(al)
	if !ok || ns != "shop-ns" {
		t.Fatalf("ns=%q ok=%v", ns, ok)
	}
	if ir.Spec.Routes[0].Priority != MinPriority {
		t.Fatalf("priority not floored: %d", ir.Spec.Routes[0].Priority)
	}
	labels := ir.Metadata["labels"].(map[string]string)
	if labels[ManagedLabel] != "true" {
		t.Fatal("managed label missing — the only thing that makes deletion safe")
	}
	if ir.Spec.Routes[0].Match != "Host(`shop.example.com`)" {
		t.Fatalf("match: %s", ir.Spec.Routes[0].Match)
	}

	// wildcard form
	al.Spec.Wildcard = true
	_, ir, _ = a.desired(al)
	if !strings.Contains(ir.Spec.Routes[0].Match, "HostRegexp(`^[a-z0-9-]+\\.shop\\.example\\.com$`)") {
		t.Fatalf("wildcard match: %s", ir.Spec.Routes[0].Match)
	}

	// allocations without service backends are not actuated
	al.Spec.Routes = []core.Route{{Backend: core.Backend{Kind: "address", Address: &core.AddressBackend{Host: "x", Port: 1}}}}
	if _, _, ok := a.desired(al); ok {
		t.Fatal("address backend must not produce an IngressRoute")
	}
}

func TestSpecEqual(t *testing.T) {
	mk := func(prio int) irSpec {
		return irSpec{EntryPoints: []string{"websecure"},
			Routes: []irRoute{{Match: "Host(`a`)", Kind: "Rule", Priority: prio, Services: []irService{{Name: "s", Port: 80}}}}}
	}
	if !specEqual(mk(10), mk(10)) {
		t.Fatal("equal specs reported unequal")
	}
	if specEqual(mk(10), mk(11)) {
		t.Fatal("priority drift missed")
	}
}
