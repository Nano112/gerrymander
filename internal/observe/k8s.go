package observe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nano112/gerrymander/internal/actuate"
	"github.com/Nano112/gerrymander/internal/api"
	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/store"
)

// K8sConfig points the observer at a cluster. In-cluster detection fills the
// zero value; out-of-cluster runs provide APIServer+TokenFile explicitly.
type K8sConfig struct {
	APIServer string // e.g. https://10.43.0.1:443
	TokenFile string
	CAFile    string
	Insecure  bool
}

// InClusterConfig returns the standard service-account config, or an error
// when not running in a pod.
func InClusterConfig() (K8sConfig, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" {
		return K8sConfig{}, errors.New("not in cluster")
	}
	return K8sConfig{
		APIServer: "https://" + host + ":" + port,
		TokenFile: "/var/run/secrets/kubernetes.io/serviceaccount/token",
		CAFile:    "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
	}, nil
}

// Observer polls cluster routes, imports platform hosts, and reports
// conflicts. It never deletes or mutates anything it observes.
type Observer struct {
	Store *store.Store
	Cfg   K8sConfig
	// Zones to manage, by name. Hosts outside all zones are ignored.
	Zones []string
	// AutoRegister creates kind=platform allocations for unregistered
	// observed hosts. When false, they are reported instead.
	AutoRegister bool
	Interval     time.Duration
	Log          *slog.Logger

	client *http.Client
	mu     sync.Mutex
	found  []Conflict
}

// Conflicts implements api.ConflictReporter.
func (o *Observer) Conflicts() []map[string]any {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]map[string]any, 0, len(o.found))
	for _, c := range o.found {
		b, _ := json.Marshal(c)
		var m map[string]any
		json.Unmarshal(b, &m)
		out = append(out, m)
	}
	return out
}

func (o *Observer) httpClient() (*http.Client, error) {
	if o.client != nil {
		return o.client, nil
	}
	tlsCfg := &tls.Config{InsecureSkipVerify: o.Cfg.Insecure}
	if o.Cfg.CAFile != "" {
		ca, err := os.ReadFile(o.Cfg.CAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(ca)
		tlsCfg.RootCAs = pool
	}
	o.client = &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	return o.client, nil
}

func (o *Observer) get(ctx context.Context, path string, into any) error {
	client, err := o.httpClient()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", o.Cfg.APIServer+path, nil)
	if err != nil {
		return err
	}
	if o.Cfg.TokenFile != "" {
		tok, err := os.ReadFile(o.Cfg.TokenFile)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// Minimal shapes for list responses.
type ingressRouteList struct {
	Items []struct {
		Metadata struct {
			Namespace string            `json:"namespace"`
			Name      string            `json:"name"`
			Labels    map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			Routes []struct {
				Match    string `json:"match"`
				Priority int    `json:"priority"`
				Services []struct {
					Name string          `json:"name"`
					Port json.RawMessage `json:"port"`
				} `json:"services"`
			} `json:"routes"`
		} `json:"spec"`
	} `json:"items"`
}

type ingressList struct {
	Items []struct {
		Metadata struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Rules []struct {
				Host string `json:"host"`
			} `json:"rules"`
		} `json:"spec"`
	} `json:"items"`
}

// FetchRoutes lists IngressRoutes + Ingresses as ObservedRoutes.
func (o *Observer) FetchRoutes(ctx context.Context) ([]ObservedRoute, error) {
	var routes []ObservedRoute
	var irs ingressRouteList
	if err := o.get(ctx, "/apis/traefik.io/v1alpha1/ingressroutes?limit=1000", &irs); err != nil {
		return nil, fmt.Errorf("ingressroutes: %w", err)
	}
	for _, item := range irs.Items {
		for _, r := range item.Spec.Routes {
			or := ObservedRoute{
				Namespace: item.Metadata.Namespace, Name: item.Metadata.Name,
				Kind: "IngressRoute", Match: r.Match, Priority: r.Priority,
				Hosts:   ParseMatch(r.Match),
				Managed: item.Metadata.Labels[actuate.ManagedLabel] == "true",
			}
			if len(r.Services) > 0 {
				or.Service = item.Metadata.Namespace + "/" + r.Services[0].Name
			}
			routes = append(routes, or)
		}
	}
	var ings ingressList
	if err := o.get(ctx, "/apis/networking.k8s.io/v1/ingresses?limit=1000", &ings); err != nil {
		return nil, fmt.Errorf("ingresses: %w", err)
	}
	for _, item := range ings.Items {
		seen := map[string]bool{}
		for _, rule := range item.Spec.Rules {
			if rule.Host == "" || seen[rule.Host] {
				continue
			}
			seen[rule.Host] = true
			routes = append(routes, ObservedRoute{
				Namespace: item.Metadata.Namespace, Name: item.Metadata.Name,
				Kind: "Ingress", Match: "Host(`" + rule.Host + "`)",
				Hosts: []HostPattern{{Host: strings.ToLower(rule.Host), Exact: true, Raw: "ingress"}},
			})
		}
	}
	return routes, nil
}

// Sync runs one observe cycle: import, conflict detection, shadow check.
func (o *Observer) Sync(ctx context.Context) error {
	routes, err := o.FetchRoutes(ctx)
	if err != nil {
		return err
	}
	return o.Reconcile(ctx, routes)
}

// Reconcile applies one snapshot of routes (separated from Sync for tests).
func (o *Observer) Reconcile(ctx context.Context, routes []ObservedRoute) error {
	var conflicts []Conflict
	unregistered := map[string]int{}

	for _, zoneName := range o.Zones {
		zone, err := o.Store.GetZone(ctx, zoneName)
		if err != nil {
			return err
		}
		seenLabels := map[string]string{} // label → ns/name
		for _, r := range routes {
			if r.Managed {
				continue // gerry's own actuator output — never classify it
			}
			for _, h := range r.Hosts {
				var label string
				var ok bool
				switch {
				case h.Exact:
					label, ok = LabelForZone(h.Host, zoneName)
				case h.CatchAll && h.Suffix != zoneName && strings.HasSuffix(h.Suffix, "."+zoneName):
					// e.g. ^[a-z0-9-]+\.cb\.app\.olsyn\.com$ → "*.cb.app"
					label, ok = "*."+strings.TrimSuffix(h.Suffix, "."+zoneName), true
				case h.CatchAll && h.Suffix == zoneName:
					continue // the tenant catch-all itself is not an ownable label
				default:
					continue
				}
				if !ok {
					continue
				}
				if prev, dup := seenLabels[label]; dup && prev != r.Namespace+"/"+r.Name {
					// Same hostname served by two different route objects —
					// worth surfacing but common with path splits; only
					// flag when the two routes hit different namespaces.
					if strings.Split(prev, "/")[0] != r.Namespace {
						conflicts = append(conflicts, Conflict{
							Zone: zoneName, Type: "duplicate-route", Label: label,
							Detail: "hostname served from two namespaces", Route: r.Namespace + "/" + r.Name, Related: prev,
						})
					}
					continue
				}
				seenLabels[label] = r.Namespace + "/" + r.Name

				existing, err := o.Store.GetAllocationByLabel(ctx, zoneName, label)
				switch {
				case errors.Is(err, store.ErrNotFound):
					if o.AutoRegister {
						_, cerr := o.Store.CreateAllocation(ctx, core.Allocation{
							ZoneID: zone.ID, Label: label, FQDN: core.FQDN(label, zoneName),
							Kind: core.KindPlatform, Source: core.SourceObserved, State: core.StateActive,
							OwnerRef: r.Namespace + "/" + r.Name, OwnerKind: r.Kind,
							Labels: map[string]string{"observed-service": r.Service},
						})
						if cerr != nil && !errors.Is(cerr, store.ErrTaken) {
							o.logf("auto-register %s.%s: %v", label, zoneName, cerr)
						}
					} else {
						unregistered[zoneName]++
					}
				case err != nil:
					return err
				case existing.Kind == core.KindTenant:
					conflicts = append(conflicts, Conflict{
						Zone: zoneName, Type: "kind-mismatch", Label: label,
						Detail:  "cluster route serves a hostname allocated to a tenant",
						Route:   r.Namespace + "/" + r.Name,
						Related: "tenant owner_ref=" + existing.OwnerRef,
					})
				}
			}
		}
		conflicts = append(conflicts, ShadowCheck(zoneName, routes)...)
	}

	sort.Slice(conflicts, func(i, j int) bool {
		return conflicts[i].Zone+conflicts[i].Label < conflicts[j].Zone+conflicts[j].Label
	})
	o.mu.Lock()
	o.found = conflicts
	o.mu.Unlock()

	perZone := map[string]int{}
	for _, c := range conflicts {
		perZone[c.Zone]++
	}
	for _, z := range o.Zones {
		api.SetConflictGauge(z, perZone[z])
		api.SetUnregisteredGauge(z, unregistered[z])
	}
	return nil
}

// Run polls until ctx is done.
func (o *Observer) Run(ctx context.Context) {
	if o.Interval <= 0 {
		o.Interval = time.Minute
	}
	t := time.NewTicker(o.Interval)
	defer t.Stop()
	for {
		if err := o.Sync(ctx); err != nil {
			o.logf("observe: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (o *Observer) logf(format string, args ...any) {
	if o.Log != nil {
		o.Log.Warn(fmt.Sprintf(format, args...))
	}
}
