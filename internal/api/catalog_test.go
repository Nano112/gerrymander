package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Nano112/gerrymander/catalog"
)

func TestCatalogScopes(t *testing.T) {
	ts := testServer(t, "root")
	resp, body := doJSON(t, "POST", ts.URL+"/v1/tokens", "root", `{"name":"reader","scope":"catalog-read"}`)
	if resp.StatusCode != 201 {
		t.Fatalf("mint: %d %v", resp.StatusCode, body)
	}
	key := body["plaintext"].(string)
	for _, req := range []struct {
		method, path, body string
		code               int
	}{
		{"GET", "/v1/catalog", "", 200}, {"GET", "/v1/catalog/access?environment=production", "", 200},
		{"GET", "/v1/catalog/access", "", 400}, {"POST", "/v1/catalog/apply", "{}", 403},
		{"POST", "/v1/catalog/assets", "{}", 403}, {"POST", "/v1/catalog/deployments/x/deactivate", "", 403},
		{"GET", "/v1/allocations", "", 403}, {"GET", "/v1/zones", "", 403},
		{"POST", "/v1/claims", `{"zone":"olsyn.com","label":"oops"}`, 403},
		{"GET", "/v1/dns", "", 403}, {"POST", "/v1/manifest/apply", "{}", 403},
		{"POST", "/v1/ports", "{}", 403}, {"GET", "/v1/tokens", "", 403},
	} {
		resp, body := doJSON(t, req.method, ts.URL+req.path, key, req.body)
		if resp.StatusCode != req.code {
			t.Errorf("%s %s: %d %v", req.method, req.path, resp.StatusCode, body)
		}
	}
	_, owner := doJSON(t, "POST", ts.URL+"/v1/tokens", "root", `{"name":"owner","scope":"owner","owner_ref":"tenant:one"}`)
	resp, _ = doJSON(t, "GET", ts.URL+"/v1/catalog", owner["plaintext"].(string), "")
	if resp.StatusCode != 403 {
		t.Fatal("owner read registry catalog")
	}
	doJSON(t, "DELETE", ts.URL+"/v1/tokens/reader", "root", "")
	resp, _ = doJSON(t, "GET", ts.URL+"/v1/catalog", key, "")
	if resp.StatusCode != 401 {
		t.Fatal("revoked catalog reader accepted")
	}
}

func TestCatalogSDKAndAssets(t *testing.T) {
	ts := testServer(t, "root")
	c := catalog.NewClient(ts.URL, "root")
	ctx := t.Context()
	data := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0L1 1"/></svg>`)
	a, err := c.UploadAsset(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	b := catalog.Bundle{SchemaVersion: 1, Application: catalog.Application{ID: "demo", Project: "demo", Display: catalog.Display{Name: "Demo", Logo: a.ID}}}
	if err := c.Apply(ctx, b); err != nil {
		t.Fatal(err)
	}
	snap, err := c.Snapshot(ctx, "demo")
	if err != nil || len(snap.Applications) != 1 || snap.Applications[0].Display.Logo != a.ID {
		t.Fatalf("SDK snapshot: %+v %v", snap, err)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/v1/catalog/assets/"+a.ID, nil)
	req.Header.Set("Authorization", "Bearer root")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(raw, data) || resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("unsafe asset response")
	}
	code, _ := doJSON(t, "POST", ts.URL+"/v1/catalog/apply", "root", `{"schema_version":1,"credentials":{"token":"never-store-this"}}`)
	if code.StatusCode != 400 {
		t.Fatal("accepted unknown secret fields")
	}
}

func TestManifestCatalogWorkspacesAndPruning(t *testing.T) {
	ts := testServer(t, "")
	c := catalog.NewClient(ts.URL, "")
	ctx := t.Context()
	yaml := `project: app
workspace: alice
environment: development
zone: alice.test
services:
  web:
    hostnames: [alice.test]
    port_pool: dev
  worker:
    catalog_only: true
resources:
  db:
    kind: database
    services: [web]
`
	if code, out := applyManifest(t, ts.URL, yaml); code != 200 {
		t.Fatalf("alice: %d %v", code, out)
	}
	bob := strings.ReplaceAll(strings.ReplaceAll(yaml, "alice.test", "bob.test"), "workspace: alice", "workspace: bob")
	if code, out := applyManifest(t, ts.URL, bob); code != 200 {
		t.Fatalf("bob: %d %v", code, out)
	}
	// A different workspace cannot seize Alice's hostnames even for the same app.
	if code, _ := applyManifest(t, ts.URL, strings.ReplaceAll(yaml, "workspace: alice", "workspace: charlie")); code != 409 {
		t.Fatalf("cross-workspace overwrite: %d", code)
	}
	snap, err := c.Snapshot(ctx, "")
	if err != nil || len(snap.Applications) != 1 || len(snap.Services) != 2 || len(snap.Deployments) != 4 || len(snap.Resources) != 2 {
		t.Fatalf("catalog counts: %+v %v", snap, err)
	}
	var ports struct {
		Allocations []struct {
			OwnerRef string `json:"owner_ref"`
			Project  string `json:"project"`
		}
	}
	r, _ := http.Get(ts.URL + "/v1/allocations")
	json.NewDecoder(r.Body).Decode(&ports)
	r.Body.Close()
	for _, a := range ports.Allocations {
		if a.OwnerRef != a.Project+"/web" {
			t.Fatal("legacy ownership leaked into workspace")
		}
	}
	withoutWorker := strings.Replace(yaml, "  worker:\n    catalog_only: true\n", "", 1)
	if code, out := applyManifest(t, ts.URL, withoutWorker); code != 200 {
		t.Fatalf("prune: %d %v", code, out)
	}
	snap, _ = c.Snapshot(ctx, "")
	inactive := 0
	for _, d := range snap.Deployments {
		if d.Lifecycle == "inactive" {
			inactive++
			if d.Workspace != "alice" {
				t.Fatal("pruned another workspace")
			}
		}
	}
	if inactive != 1 {
		t.Fatalf("inactive: %d", inactive)
	}
}

func TestManifestCatalogPreflightBeforeRouting(t *testing.T) {
	ts := testServer(t, "")
	yaml := `project: demo
zone: demo.test
services:
  web:
    hostnames: [demo.test]
    address: localhost:8000
    capabilities:
      version: v1
      entry_permission: access
      permissions: {access: Open}
    deployment:
      capability_version: v1
`
	if code, out := applyManifest(t, ts.URL, yaml); code != 200 {
		t.Fatalf("first: %d %v", code, out)
	}
	mutated := strings.ReplaceAll(strings.ReplaceAll(yaml, "access: Open", "access: Changed"), "demo.test]", "changed.demo.test]")
	if code, _ := applyManifest(t, ts.URL, mutated); code != 409 {
		t.Fatalf("version rewrite accepted: %d", code)
	}
	_, out := doJSON(t, "GET", ts.URL+"/v1/allocations", "", "")
	allocs := out["allocations"].([]any)
	if len(allocs) != 1 || allocs[0].(map[string]any)["fqdn"] != "demo.test" {
		t.Fatal("preflight failure mutated routes")
	}
}

func TestCatalogOnlyDoesNotPruneRoutes(t *testing.T) {
	ts := testServer(t, "")
	if code, out := applyManifest(t, ts.URL, applyV1); code != 200 {
		t.Fatalf("routing setup: %d %v", code, out)
	}
	if code, out := applyManifest(t, ts.URL, "project: coolsite\nresources:\n  uploads:\n    kind: object-storage\n"); code != 200 {
		t.Fatalf("catalog only: %d %v", code, out)
	}
	_, out := doJSON(t, "GET", ts.URL+"/v1/allocations", "", "")
	if len(out["allocations"].([]any)) != 2 {
		t.Fatal("catalog-only declaration pruned routes")
	}
	// An explicit zone can classify metadata; it must not imply routing ownership.
	if code, out := applyManifest(t, ts.URL, "project: coolsite\nzone: coolsite.test\nresources:\n  uploads:\n    kind: object-storage\n"); code != 200 {
		t.Fatalf("zoned resources: %d %v", code, out)
	}
	snap, err := catalog.NewClient(ts.URL, "").Snapshot(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range snap.Deployments {
		if d.Lifecycle != "registered" {
			t.Fatal("resource update retired existing service")
		}
	}
	if code, out := applyManifest(t, ts.URL, "project: coolsite\nzone: coolsite.test\nservices:\n  external:\n    catalog_only: true\n"); code != 200 {
		t.Fatalf("zoned metadata: %d %v", code, out)
	}
	_, out = doJSON(t, "GET", ts.URL+"/v1/allocations", "", "")
	if len(out["allocations"].([]any)) != 2 {
		t.Fatal("zoned metadata pruned routes")
	}
}
