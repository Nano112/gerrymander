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
	"net/url"
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
// conflicts. It never mutates Kubernetes resources. Optional retirement only
// releases registry records imported by this observer.
type Observer struct {
	Store *store.Store
	Cfg   K8sConfig
	// Zones to manage, by name. Hosts outside all zones are ignored.
	Zones []string
	// AutoRegister creates kind=platform allocations for unregistered
	// observed hosts. When false, they are reported instead.
	AutoRegister       bool
	RetireMissing      bool
	MissingGracePeriod time.Duration
	Now                func() time.Time
	cycleMu            sync.Mutex
	lastSuccess        time.Time
	Interval           time.Duration
	Log                *slog.Logger

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
type listMetadata struct {
	Continue        string `json:"continue"`
	ResourceVersion string `json:"resourceVersion"`
}
type ingressRouteList struct {
	Metadata listMetadata `json:"metadata"`
	Items    []struct {
		Metadata struct {
			UID       string            `json:"uid"`
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
	Metadata listMetadata `json:"metadata"`
	Items    []struct {
		Metadata struct {
			UID       string `json:"uid"`
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
	for _, resource := range []string{"/apis/traefik.io/v1alpha1/ingressroutes", "/apis/networking.k8s.io/v1/ingresses"} {
		continuation, version := "", ""
		tokens := map[string]bool{}
		for {
			path := resource + "?limit=1000"
			if continuation != "" {
				path += "&continue=" + url.QueryEscape(continuation)
			}
			var meta listMetadata
			if strings.HasSuffix(resource, "/ingressroutes") {
				var list ingressRouteList
				if err := o.get(ctx, path, &list); err != nil {
					return nil, err
				}
				if list.Items == nil {
					return nil, fmt.Errorf("%s: missing items in list", resource)
				}
				meta = list.Metadata
				for _, item := range list.Items {
					for _, r := range item.Spec.Routes {
						observed := ObservedRoute{
							Namespace: item.Metadata.Namespace, Name: item.Metadata.Name, UID: item.Metadata.UID,
							Kind: "IngressRoute", Match: r.Match, Priority: r.Priority,
							Hosts: ParseMatch(r.Match), Managed: item.Metadata.Labels[actuate.ManagedLabel] == "true",
						}
						if len(r.Services) > 0 {
							observed.Service = item.Metadata.Namespace + "/" + r.Services[0].Name
						}
						routes = append(routes, observed)
					}
				}
			} else {
				var list ingressList
				if err := o.get(ctx, path, &list); err != nil {
					return nil, err
				}
				if list.Items == nil {
					return nil, fmt.Errorf("%s: missing items in list", resource)
				}
				meta = list.Metadata
				for _, item := range list.Items {
					seen := map[string]bool{}
					for _, rule := range item.Spec.Rules {
						if rule.Host == "" || seen[rule.Host] {
							continue
						}
						seen[rule.Host] = true
						routes = append(routes, ObservedRoute{
							Namespace: item.Metadata.Namespace, Name: item.Metadata.Name, UID: item.Metadata.UID,
							Kind: "Ingress", Match: "Host(`" + rule.Host + "`)",
							Hosts: []HostPattern{{Host: strings.ToLower(rule.Host), Exact: true, Raw: "ingress"}},
						})
					}
				}
			}
			if continuation != "" && meta.ResourceVersion != version {
				return nil, fmt.Errorf("%s: resource version changed during pagination", resource)
			}
			version = meta.ResourceVersion
			if meta.Continue == "" {
				break
			}
			if tokens[meta.Continue] {
				return nil, fmt.Errorf("%s: repeated continuation token", resource)
			}
			tokens[meta.Continue] = true
			continuation = meta.Continue
		}
	}
	return routes, nil
}

// Sync runs one observe cycle: import, conflict detection, shadow check.
func (o *Observer) Sync(ctx context.Context) error {
	o.cycleMu.Lock()
	defer o.cycleMu.Unlock()
	routes, err := o.FetchRoutes(ctx)
	if err == nil {
		err = o.reconcile(ctx, routes)
	}
	if err != nil {
		o.lastSuccess = time.Time{}
	}
	api.RecordObserverSync(err == nil)
	return err
}

// Reconcile applies a complete successful snapshot (separated for tests).
func (o *Observer) Reconcile(ctx context.Context, routes []ObservedRoute) error {
	o.cycleMu.Lock()
	defer o.cycleMu.Unlock()
	err := o.reconcile(ctx, routes)
	if err != nil {
		o.lastSuccess = time.Time{}
	}
	return err
}

func (o *Observer) reconcile(ctx context.Context, routes []ObservedRoute) error {
	now := time.Now().UTC()
	if o.Now != nil {
		now = o.Now().UTC()
	}
	interval := o.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	grace := o.MissingGracePeriod
	if grace <= 0 {
		grace = 5 * time.Minute
	}
	resetMissing := o.lastSuccess.IsZero() || now.Before(o.lastSuccess) || now.Sub(o.lastSuccess) > 2*interval
	var conflicts []Conflict
	unregistered := map[string]int{}

	for _, zoneName := range o.Zones {
		zone, err := o.Store.GetZone(ctx, zoneName)
		if err != nil {
			return err
		}
		seenLabels := map[string]string{} // label → ns/name
		present := map[string]store.ObservedOwner{}
		for _, r := range routes {
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
				if _, exists := present[label]; !exists || !r.Managed {
					present[label] = store.ObservedOwner{Ref: r.Namespace + "/" + r.Name, Kind: r.Kind, UID: r.UID, Service: r.Service}
				}
				if r.Managed {
					// Keep a live host, but never import/classify actuator output.
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
							return fmt.Errorf("auto-register %s.%s: %w", label, zoneName, cerr)
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
		if o.AutoRegister {
			result, err := o.Store.ReconcileObservations(ctx, zoneName, present, now, grace, o.RetireMissing, resetMissing)
			if err != nil {
				return fmt.Errorf("reconcile observed hosts in %s: %w", zoneName, err)
			}
			api.SetObservationMetrics(zoneName, result.Missing, result.Retired)
			if result.Retired > 0 {
				o.logf("retired %d missing observed hosts in %s (%d awaiting grace)", result.Retired, zoneName, result.Missing)
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
	o.lastSuccess = now
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
