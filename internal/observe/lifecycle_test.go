package observe

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/store"
)

func lifecycleObserver(t *testing.T) (*Observer, *time.Time) {
	t.Helper()
	st, err := store.Open("sqlite:" + filepath.Join(t.TempDir(), "lifecycle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, zone := range []string{"example.com", "dev.example.com"} {
		if _, err := st.EnsureZone(t.Context(), core.Zone{Name: zone}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	return &Observer{Store: st, Zones: []string{"example.com", "dev.example.com"}, AutoRegister: true, RetireMissing: true, MissingGracePeriod: 5 * time.Minute, Now: func() time.Time { return now }}, &now
}

func observedHost(host, name, uid string) ObservedRoute {
	return ObservedRoute{Namespace: "sessions", Name: name, UID: uid, Kind: "Ingress", Hosts: []HostPattern{{Host: host, Exact: true}}}
}

func TestObservedLifecycleRetiresAndRediscovers(t *testing.T) {
	o, now := lifecycleObserver(t)
	ctx := t.Context()
	route := observedHost("stream-123.example.com", "session", "uid-1")
	if err := o.Reconcile(ctx, []ObservedRoute{route}); err != nil {
		t.Fatal(err)
	}
	a, err := o.Store.GetAllocationByLabel(ctx, "example.com", "stream-123")
	if err != nil || a.Status.Observation == nil || a.Status.Observation.OwnerUID != "uid-1" {
		t.Fatalf("missing identity: %+v %v", a, err)
	}
	for minute := 1; minute <= 6; minute++ {
		*now = now.Add(time.Minute)
		if err := o.Reconcile(ctx, nil); err != nil {
			t.Fatal(err)
		}
		_, err := o.Store.GetAllocationByLabel(ctx, "example.com", "stream-123")
		if minute < 6 && err != nil {
			t.Fatalf("retired before five minute grace: %v", err)
		}
		if minute == 6 && !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("not retired: %v", err)
		}
	}
	*now = now.Add(time.Minute)
	route.Name, route.UID = "replacement", "uid-2"
	if err := o.Reconcile(ctx, []ObservedRoute{route}); err != nil {
		t.Fatal(err)
	}
	a, err = o.Store.GetAllocationByLabel(ctx, "example.com", "stream-123")
	if err != nil || a.OwnerRef != "sessions/replacement" || a.Status.Observation.OwnerUID != "uid-2" || a.Status.Observation.MissingSince != nil {
		t.Fatalf("rediscovery: %+v %v", a, err)
	}
}

func TestObservedLifecyclePresenceIdentityAndScope(t *testing.T) {
	o, now := lifecycleObserver(t)
	ctx := t.Context()
	routes := []ObservedRoute{
		observedHost("host.dev.example.com", "old", "old-uid"),
		observedHost("*.dev.example.com", "wildcard", "wildcard-uid"),
	}
	if err := o.Reconcile(ctx, routes); err != nil {
		t.Fatal(err)
	}
	z, _ := o.Store.GetZone(ctx, "example.com")
	for _, a := range []core.Allocation{
		{Label: "api", Source: core.SourceAPI, Kind: core.KindPlatform},
		{Label: "seed", Source: core.SourceSeed, Kind: core.KindPlatform},
		{Label: "tenant", Source: core.SourceObserved, Kind: core.KindTenant},
		{Label: "intent", Source: core.SourceObserved, Kind: core.KindPlatform, Spec: core.Spec{Routes: []core.Route{{}}}},
	} {
		a.ZoneID, a.State, a.FQDN = z.ID, core.StateActive, a.Label+".example.com"
		if _, err := o.Store.CreateAllocation(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 8; i++ {
		*now = now.Add(time.Minute)
		replacement := observedHost("host.dev.example.com", "new", "new-uid")
		if err := o.Reconcile(ctx, []ObservedRoute{replacement, routes[1]}); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range [][2]string{{"example.com", "host.dev"}, {"dev.example.com", "host"}, {"example.com", "*.dev"}, {"dev.example.com", "*"}, {"example.com", "api"}, {"example.com", "seed"}, {"example.com", "tenant"}, {"example.com", "intent"}} {
		a, err := o.Store.GetAllocationByLabel(ctx, key[0], key[1])
		if err != nil {
			t.Fatalf("lost %v: %v", key, err)
		}
		if key[1] == "host" && (a.OwnerRef != "sessions/new" || a.Status.Observation.OwnerUID != "new-uid") {
			t.Fatalf("owner not refreshed: %+v", a)
		}
	}
}

func TestObservedLifecycleResetsAbsence(t *testing.T) {
	for _, reset := range []string{"rediscovered", "restart", "gap", "failure", "disabled"} {
		t.Run(reset, func(t *testing.T) {
			o, now := lifecycleObserver(t)
			route := observedHost("gone.example.com", "old", "uid")
			if err := o.Reconcile(t.Context(), []ObservedRoute{route}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 5; i++ {
				*now = now.Add(time.Minute)
				if err := o.Reconcile(t.Context(), nil); err != nil {
					t.Fatal(err)
				}
			}
			switch reset {
			case "rediscovered":
				if err := o.Reconcile(t.Context(), []ObservedRoute{route}); err != nil {
					t.Fatal(err)
				}
			case "restart":
				o = &Observer{Store: o.Store, Zones: o.Zones, AutoRegister: true, RetireMissing: true, Now: o.Now}
			case "gap":
				*now = now.Add(time.Hour)
			case "failure":
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) }))
				defer server.Close()
				o.Cfg.APIServer = server.URL
				if err := o.Sync(t.Context()); err == nil {
					t.Fatal("expected failed scan")
				}
			case "disabled":
				o.RetireMissing = false
			}
			*now = now.Add(time.Minute)
			if err := o.Reconcile(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			a, err := o.Store.GetAllocationByLabel(t.Context(), "example.com", "gone")
			if err != nil {
				t.Fatalf("unsafe retirement after %s: %v", reset, err)
			}
			if reset != "disabled" && a.Status.Observation.MissingObservations != 1 {
				t.Fatalf("absence not reset: %+v", a.Status.Observation)
			}
		})
	}
}

func TestFetchRoutesPaginationAndPartialFailures(t *testing.T) {
	for _, mode := range []string{"complete", "page-error", "second-kind-error", "repeated-token", "changed-version", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			o, now := lifecycleObserver(t)
			route := observedHost("keep.example.com", "old", "uid")
			if err := o.Reconcile(t.Context(), []ObservedRoute{route}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 5; i++ {
				*now = now.Add(time.Minute)
				if err := o.Reconcile(t.Context(), nil); err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "malformed" {
					w.Write([]byte("{}"))
					return
				}
				isIngress := r.URL.Path == "/apis/networking.k8s.io/v1/ingresses"
				next := r.URL.Query().Get("continue")
				if (mode == "page-error" && next != "") || (mode == "second-kind-error" && isIngress) {
					http.Error(w, "unavailable", 503)
					return
				}
				token := "a+/=&"
				version := "1"
				items := []any{}
				if next != "" {
					if next != "a+/=&" {
						t.Errorf("continuation corrupted: %q", next)
					}
					token = ""
					if mode == "repeated-token" {
						token = next
					}
					if mode == "changed-version" {
						version = "2"
					}
					meta := map[string]any{"namespace": "sessions", "name": "replacement", "uid": "uid-2"}
					if isIngress {
						items = append(items, map[string]any{"metadata": meta, "spec": map[string]any{"rules": []any{map[string]any{"host": "keep.example.com"}}}})
					} else {
						items = append(items, map[string]any{"metadata": meta, "spec": map[string]any{"routes": []any{map[string]any{"match": "Host(`other.example.com`)"}}}})
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"continue": token, "resourceVersion": version}, "items": items})
			}))
			defer server.Close()
			o.Cfg.APIServer = server.URL
			*now = now.Add(time.Minute)
			err := o.Sync(t.Context())
			if mode == "complete" && err != nil {
				t.Fatal(err)
			}
			if mode != "complete" && err == nil {
				t.Fatal("accepted incomplete snapshot")
			}
			a, err := o.Store.GetAllocationByLabel(t.Context(), "example.com", "keep")
			if err != nil {
				t.Fatalf("lost live host on later page or failed scan: %v", err)
			}
			if mode == "complete" && a.Status.Observation.OwnerUID != "uid-2" {
				t.Fatal("did not process second page")
			}
			if mode != "complete" {
				*now = now.Add(time.Minute)
				if err := o.Reconcile(t.Context(), nil); err != nil {
					t.Fatal(err)
				}
				if _, err := o.Store.GetAllocationByLabel(t.Context(), "example.com", "keep"); err != nil {
					t.Fatal("failure did not reset grace")
				}
			}
		})
	}
}

func TestManagedAndDuplicateOwnersKeepObservedHost(t *testing.T) {
	o, now := lifecycleObserver(t)
	route := observedHost("keep.example.com", "old", "old-uid")
	if err := o.Reconcile(t.Context(), []ObservedRoute{route}); err != nil {
		t.Fatal(err)
	}
	managed := observedHost("keep.example.com", "managed", "managed-uid")
	managed.Managed = true
	other := observedHost("keep.example.com", "other", "other-uid")
	other.Namespace = "replacement"
	for i := 0; i < 8; i++ {
		*now = now.Add(time.Minute)
		routes := []ObservedRoute{managed}
		if i%2 == 0 {
			routes = append(routes, other)
		}
		if err := o.Reconcile(t.Context(), routes); err != nil {
			t.Fatal(err)
		}
	}
	a, err := o.Store.GetAllocationByLabel(t.Context(), "example.com", "keep")
	if err != nil || a.Status.Observation.MissingSince != nil {
		t.Fatalf("lost still served host: %+v %v", a, err)
	}
}
