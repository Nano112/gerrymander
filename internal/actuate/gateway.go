package actuate

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/k8slite"
	"github.com/Nano112/gerrymander/internal/store"
)

// GatewayActuator materializes Gateway API HTTPRoutes for allocations that
// carry service backends — the standards-track sibling of the Traefik
// provider. Same safety contract: it lists and mutates ONLY resources
// labeled app.gerrymander/managed=true, and wildcard hostnames map to the
// Gateway API's native "*." form (no regex, no priority trap — precise
// hostnames win by spec).
type GatewayActuator struct {
	Store    *store.Store
	Client   *k8slite.Client
	Zones    []string
	Interval time.Duration
	// Gateway the routes attach to (parentRef).
	GatewayName      string
	GatewayNamespace string
	Log              *slog.Logger
}

const gwBase = "/apis/gateway.networking.k8s.io/v1"

type httpRoute struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Metadata   map[string]any `json:"metadata"`
	Spec       hrSpec         `json:"spec"`
}

type hrSpec struct {
	ParentRefs []hrParentRef `json:"parentRefs"`
	Hostnames  []string      `json:"hostnames"`
	Rules      []hrRule      `json:"rules"`
}

type hrParentRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

type hrRule struct {
	BackendRefs []hrBackendRef `json:"backendRefs"`
}

type hrBackendRef struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

func (g *GatewayActuator) desired(al core.Allocation) (ns string, hr httpRoute, ok bool) {
	if al.State != core.StateActive || len(al.Spec.Routes) == 0 {
		return "", httpRoute{}, false
	}
	b := al.Spec.Routes[0].Backend
	if b.Kind != "service" || b.Service == nil {
		return "", httpRoute{}, false
	}
	hostnames := []string{al.FQDN}
	if al.Spec.Wildcard {
		hostnames = append(hostnames, "*."+al.FQDN)
	}
	name := RouteName(al.ZoneName, al.Label)
	hr = httpRoute{
		APIVersion: "gateway.networking.k8s.io/v1",
		Kind:       "HTTPRoute",
		Metadata: map[string]any{
			"name":      name,
			"namespace": b.Service.Namespace,
			"labels":    map[string]string{ManagedLabel: "true"},
			"annotations": map[string]string{
				"app.gerrymander/zone":  al.ZoneName,
				"app.gerrymander/label": al.Label,
				"app.gerrymander/owner": al.OwnerRef,
			},
		},
		Spec: hrSpec{
			ParentRefs: []hrParentRef{{Name: g.GatewayName, Namespace: g.GatewayNamespace}},
			Hostnames:  hostnames,
			Rules:      []hrRule{{BackendRefs: []hrBackendRef{{Name: b.Service.Name, Port: b.Service.Port}}}},
		},
	}
	return b.Service.Namespace, hr, true
}

// Reconcile converges the cluster's managed HTTPRoutes to the registry.
func (g *GatewayActuator) Reconcile(ctx context.Context) error {
	type want struct {
		ns string
		hr httpRoute
	}
	desired := map[string]want{}
	for _, zone := range g.Zones {
		allocs, err := g.Store.ListAllocations(ctx, store.AllocFilter{Zone: zone, State: string(core.StateActive)})
		if err != nil {
			return err
		}
		for _, al := range allocs {
			if ns, hr, ok := g.desired(al); ok {
				desired[ns+"/"+hr.Metadata["name"].(string)] = want{ns, hr}
			}
		}
	}

	var existing struct {
		Items []struct {
			Metadata struct {
				Namespace       string            `json:"namespace"`
				Name            string            `json:"name"`
				ResourceVersion string            `json:"resourceVersion"`
				Labels          map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec hrSpec `json:"spec"`
		} `json:"items"`
	}
	if err := g.Client.Do(ctx, "GET", gwBase+"/httproutes?labelSelector="+ManagedLabel+"%3Dtrue&limit=1000", nil, &existing); err != nil {
		return fmt.Errorf("list managed httproutes: %w", err)
	}

	seen := map[string]bool{}
	for _, ex := range existing.Items {
		key := ex.Metadata.Namespace + "/" + ex.Metadata.Name
		seen[key] = true
		w, ok := desired[key]
		if !ok {
			path := fmt.Sprintf("%s/namespaces/%s/httproutes/%s", gwBase, ex.Metadata.Namespace, ex.Metadata.Name)
			if err := g.Client.Do(ctx, "DELETE", path, nil, nil); err != nil {
				g.logf("delete %s: %v", key, err)
			} else {
				g.logf("removed httproute %s (allocation released)", key)
			}
			continue
		}
		if !reflect.DeepEqual(ex.Spec, w.hr.Spec) {
			w.hr.Metadata["resourceVersion"] = ex.Metadata.ResourceVersion
			path := fmt.Sprintf("%s/namespaces/%s/httproutes/%s", gwBase, w.ns, w.hr.Metadata["name"])
			if err := g.Client.Do(ctx, "PUT", path, w.hr, nil); err != nil {
				g.logf("update %s: %v", key, err)
			} else {
				g.logf("repaired httproute %s (drift)", key)
			}
		}
	}

	for key, w := range desired {
		if seen[key] {
			continue
		}
		path := fmt.Sprintf("%s/namespaces/%s/httproutes", gwBase, w.ns)
		if err := g.Client.Do(ctx, "POST", path, w.hr, nil); err != nil {
			g.logf("create %s: %v", key, err)
		} else {
			g.logf("created httproute %s", key)
		}
	}
	return nil
}

// Run reconciles on an interval until ctx ends.
func (g *GatewayActuator) Run(ctx context.Context) {
	if g.Interval <= 0 {
		g.Interval = time.Minute
	}
	t := time.NewTicker(g.Interval)
	defer t.Stop()
	for {
		if err := g.Reconcile(ctx); err != nil && ctx.Err() == nil {
			g.logf("reconcile: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (g *GatewayActuator) logf(format string, args ...any) {
	if g.Log != nil {
		g.Log.Info("actuator(gateway): " + fmt.Sprintf(format, args...))
	}
}
