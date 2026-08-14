package actuate

import (
	"testing"

	"github.com/Nano112/gerrymander/internal/core"
)

func TestGatewayDesired(t *testing.T) {
	g := &GatewayActuator{GatewayName: "edge", GatewayNamespace: "infra"}
	al := core.Allocation{
		ZoneName: "example.com", Label: "shop", FQDN: "shop.example.com",
		State: core.StateActive, OwnerRef: "tenant-9",
		Spec: core.Spec{Routes: []core.Route{{Backend: core.Backend{Kind: "service",
			Service: &core.ServiceBackend{Namespace: "shop-ns", Name: "shop-svc", Port: 8080}}}}},
	}
	ns, hr, ok := g.desired(al)
	if !ok || ns != "shop-ns" {
		t.Fatalf("ns=%q ok=%v", ns, ok)
	}
	if hr.Spec.Hostnames[0] != "shop.example.com" {
		t.Fatalf("hostnames: %v", hr.Spec.Hostnames)
	}
	if hr.Spec.ParentRefs[0].Name != "edge" || hr.Spec.ParentRefs[0].Namespace != "infra" {
		t.Fatalf("parentRefs: %v", hr.Spec.ParentRefs)
	}
	if hr.Metadata["labels"].(map[string]string)[ManagedLabel] != "true" {
		t.Fatal("managed label missing")
	}
	if hr.Spec.Rules[0].BackendRefs[0].Port != 8080 {
		t.Fatalf("backend: %v", hr.Spec.Rules)
	}

	// wildcard uses the Gateway API's native form
	al.Spec.Wildcard = true
	_, hr, _ = g.desired(al)
	if len(hr.Spec.Hostnames) != 2 || hr.Spec.Hostnames[1] != "*.shop.example.com" {
		t.Fatalf("wildcard hostnames: %v", hr.Spec.Hostnames)
	}

	// non-service backends are not actuated
	al.Spec.Routes = []core.Route{{Backend: core.Backend{Kind: "address", Address: &core.AddressBackend{Host: "x", Port: 1}}}}
	if _, _, ok := g.desired(al); ok {
		t.Fatal("address backend must not produce an HTTPRoute")
	}
}
