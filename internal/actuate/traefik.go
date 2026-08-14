// Package actuate is gerrymander's write side for Kubernetes: registry
// claims with service backends become Traefik IngressRoute objects, and
// releases remove them. Two safety rules are absolute:
//
//  1. gerry only ever creates, updates, or deletes routes carrying its own
//     label (app.gerrymander/managed=true) — hand-written routes are
//     invisible to the reconciler's write path.
//  2. generated routes always carry priority ≥ MinPriority, so they cannot
//     tie with a low-priority tenant catch-all — the shadow trap is
//     unrepresentable for managed routes.
package actuate

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/k8slite"
	"github.com/Nano112/gerrymander/internal/store"
)

const (
	ManagedLabel = "app.gerrymander/managed"
	// MinPriority keeps managed routes strictly above priority-1 catch-alls.
	MinPriority = 10
)

// Actuator reconciles registry state into IngressRoutes.
type Actuator struct {
	Store    *store.Store
	Client   *k8slite.Client
	Zones    []string
	Interval time.Duration
	// EntryPoints for generated routes (default ["websecure"]).
	EntryPoints []string
	Log         *slog.Logger
}

// ingressRoute is the minimal Traefik CRD shape we read/write.
type ingressRoute struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Metadata   map[string]any `json:"metadata"`
	Spec       irSpec         `json:"spec"`
}

type irSpec struct {
	EntryPoints []string  `json:"entryPoints"`
	Routes      []irRoute `json:"routes"`
}

type irRoute struct {
	Match    string      `json:"match"`
	Kind     string      `json:"kind"`
	Priority int         `json:"priority,omitempty"`
	Services []irService `json:"services"`
}

type irService struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

var unsafeName = regexp.MustCompile(`[^a-z0-9-]+`)

// RouteName is deterministic per (zone,label) so reconciles converge.
func RouteName(zone, label string) string {
	l := strings.TrimPrefix(label, "*.")
	if l == "@" {
		l = "apex"
	}
	n := "gerry-" + unsafeName.ReplaceAllString(strings.ToLower(zone+"-"+l), "-")
	if len(n) > 63 {
		n = n[:63]
	}
	return strings.Trim(n, "-")
}

// desired builds the IngressRoute for an allocation's first service-backend
// route; ok=false when the allocation has none.
func (a *Actuator) desired(al core.Allocation) (ns string, ir ingressRoute, ok bool) {
	var svc *core.ServiceBackend
	for _, r := range al.Spec.Routes {
		if r.Backend.Kind == "service" && r.Backend.Service != nil {
			svc = r.Backend.Service
			break
		}
	}
	if svc == nil {
		return "", ingressRoute{}, false
	}
	match := fmt.Sprintf("Host(`%s`)", al.FQDN)
	if al.Spec.Wildcard || strings.HasPrefix(al.Label, "*.") {
		base := strings.TrimPrefix(al.FQDN, "*.")
		match = fmt.Sprintf("Host(`%s`) || HostRegexp(`^[a-z0-9-]+\\.%s$`)", base, strings.ReplaceAll(base, ".", "\\."))
	}
	prio := al.Spec.Priority
	if prio < MinPriority {
		prio = MinPriority
	}
	eps := a.EntryPoints
	if len(eps) == 0 {
		eps = []string{"websecure"}
	}
	name := RouteName(al.ZoneName, al.Label)
	return svc.Namespace, ingressRoute{
		APIVersion: "traefik.io/v1alpha1",
		Kind:       "IngressRoute",
		Metadata: map[string]any{
			"name":      name,
			"namespace": svc.Namespace,
			"labels":    map[string]string{ManagedLabel: "true"},
			"annotations": map[string]string{
				"app.gerrymander/zone":  al.ZoneName,
				"app.gerrymander/label": al.Label,
				"app.gerrymander/owner": al.OwnerRef,
			},
		},
		Spec: irSpec{
			EntryPoints: eps,
			Routes: []irRoute{{
				Match: match, Kind: "Rule", Priority: prio,
				Services: []irService{{Name: svc.Name, Port: svc.Port}},
			}},
		},
	}, true
}

// Reconcile converges the cluster to the registry (one pass).
func (a *Actuator) Reconcile(ctx context.Context) error {
	// Desired set from the registry.
	type want struct {
		ns string
		ir ingressRoute
	}
	desired := map[string]want{} // ns/name
	for _, zone := range a.Zones {
		allocs, err := a.Store.ListAllocations(ctx, store.AllocFilter{Zone: zone, State: string(core.StateActive)})
		if err != nil {
			return err
		}
		for _, al := range allocs {
			if ns, ir, ok := a.desired(al); ok {
				desired[ns+"/"+ir.Metadata["name"].(string)] = want{ns, ir}
			}
		}
	}

	// Existing set: ONLY routes carrying our label.
	var existing struct {
		Items []struct {
			Metadata struct {
				Namespace       string            `json:"namespace"`
				Name            string            `json:"name"`
				ResourceVersion string            `json:"resourceVersion"`
				Labels          map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec irSpec `json:"spec"`
		} `json:"items"`
	}
	if err := a.Client.Do(ctx, "GET", "/apis/traefik.io/v1alpha1/ingressroutes?labelSelector="+ManagedLabel+"%3Dtrue&limit=1000", nil, &existing); err != nil {
		return fmt.Errorf("list managed routes: %w", err)
	}

	seen := map[string]bool{}
	for _, ex := range existing.Items {
		key := ex.Metadata.Namespace + "/" + ex.Metadata.Name
		seen[key] = true
		w, ok := desired[key]
		if !ok {
			// Released in the registry → remove from the cluster.
			path := fmt.Sprintf("/apis/traefik.io/v1alpha1/namespaces/%s/ingressroutes/%s", ex.Metadata.Namespace, ex.Metadata.Name)
			if err := a.Client.Do(ctx, "DELETE", path, nil, nil); err != nil {
				a.logf("delete %s: %v", key, err)
			} else {
				a.logf("removed route %s (allocation released)", key)
			}
			continue
		}
		// Drift check: compare specs.
		if !specEqual(ex.Spec, w.ir.Spec) {
			w.ir.Metadata["resourceVersion"] = ex.Metadata.ResourceVersion
			path := fmt.Sprintf("/apis/traefik.io/v1alpha1/namespaces/%s/ingressroutes/%s", w.ns, w.ir.Metadata["name"])
			if err := a.Client.Do(ctx, "PUT", path, w.ir, nil); err != nil {
				a.logf("update %s: %v", key, err)
			} else {
				a.logf("updated route %s", key)
			}
		}
	}
	// Create the missing.
	keys := make([]string, 0, len(desired))
	for k := range desired {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if seen[key] {
			continue
		}
		w := desired[key]
		path := fmt.Sprintf("/apis/traefik.io/v1alpha1/namespaces/%s/ingressroutes", w.ns)
		if err := a.Client.Do(ctx, "POST", path, w.ir, nil); err != nil {
			a.logf("create %s: %v", key, err)
		} else {
			a.logf("created route %s → %v", key, w.ir.Spec.Routes[0].Services[0])
		}
	}
	return nil
}

func specEqual(a, b irSpec) bool {
	if len(a.Routes) != len(b.Routes) || len(a.EntryPoints) != len(b.EntryPoints) {
		return false
	}
	for i := range a.EntryPoints {
		if a.EntryPoints[i] != b.EntryPoints[i] {
			return false
		}
	}
	for i := range a.Routes {
		ra, rb := a.Routes[i], b.Routes[i]
		if ra.Match != rb.Match || ra.Priority != rb.Priority || len(ra.Services) != len(rb.Services) {
			return false
		}
		for j := range ra.Services {
			if ra.Services[j] != rb.Services[j] {
				return false
			}
		}
	}
	return true
}

// Run reconciles until ctx is done.
func (a *Actuator) Run(ctx context.Context) {
	if a.Interval <= 0 {
		a.Interval = time.Minute
	}
	t := time.NewTicker(a.Interval)
	defer t.Stop()
	for {
		if err := a.Reconcile(ctx); err != nil {
			a.logf("actuate: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *Actuator) logf(format string, args ...any) {
	if a.Log != nil {
		a.Log.Info(fmt.Sprintf(format, args...))
	}
}
