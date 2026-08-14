package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const applyV1 = `
project: coolsite
zone: coolsite.test
services:
  frontend:
    hostnames: [coolsite.test, "*.coolsite.test"]
    port_pool: dev
  api:
    hostnames: [api.coolsite.test]
    port_pool: dev
`

// v2 renames api → backend.coolsite.test: apply must claim the new label and
// prune the old one. That IS the domain re-assignment workflow.
const applyV2 = `
project: coolsite
zone: coolsite.test
services:
  frontend:
    hostnames: [coolsite.test, "*.coolsite.test"]
    port_pool: dev
  api:
    hostnames: [backend.coolsite.test]
    port_pool: dev
`

func applyManifest(t *testing.T, url, yaml string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"yaml": yaml})
	resp, err := http.Post(url+"/v1/manifest/apply", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestManifestApplyLifecycle(t *testing.T) {
	ts := testServer(t, "")

	code, out := applyManifest(t, ts.URL, applyV1)
	if code != 200 {
		t.Fatalf("apply v1: %d %v", code, out)
	}
	if n := len(out["claimed"].([]any)); n != 2 {
		t.Fatalf("v1 claimed %d, want 2 (%v)", n, out["claimed"])
	}
	svcs := out["services"].(map[string]any)
	fe := svcs["frontend"].(map[string]any)
	if fe["port"].(float64) < 51000 {
		t.Fatalf("frontend port: %v", fe["port"])
	}
	if fe["wildcards"].([]any)[0] != "coolsite.test" {
		t.Fatalf("wildcards: %v", fe["wildcards"])
	}

	// re-apply is idempotent: updates, claims nothing new, prunes nothing
	code, out = applyManifest(t, ts.URL, applyV1)
	if code != 200 || len(out["claimed"].([]any)) != 0 || len(out["pruned"].([]any)) != 0 {
		t.Fatalf("re-apply: %d %v", code, out)
	}
	if len(out["updated"].([]any)) != 2 {
		t.Fatalf("re-apply should update in place: %v", out["updated"])
	}
	// sticky port survives
	fe2 := out["services"].(map[string]any)["frontend"].(map[string]any)
	if fe2["port"] != fe["port"] {
		t.Fatalf("port not sticky across applies: %v vs %v", fe2["port"], fe["port"])
	}

	// rename: api.coolsite.test → backend.coolsite.test
	code, out = applyManifest(t, ts.URL, applyV2)
	if code != 200 {
		t.Fatalf("apply v2: %d %v", code, out)
	}
	if got := out["claimed"].([]any); len(got) != 1 || got[0] != "backend.coolsite.test" {
		t.Fatalf("v2 claimed: %v", got)
	}
	if got := out["pruned"].([]any); len(got) != 1 || got[0] != "api.coolsite.test" {
		t.Fatalf("v2 pruned: %v", got)
	}

	// foreign ownership is refused, not clobbered
	_, tenant := doJSON(t, "POST", ts.URL+"/v1/claims", "", `{"zone":"coolsite.test","label":"taken-by-tenant","owner_ref":"someone"}`)
	_ = tenant
	conflict := strings.Replace(applyV2, "backend.coolsite.test", "taken-by-tenant.coolsite.test", 1)
	code, out = applyManifest(t, ts.URL, conflict)
	if code != 409 {
		t.Fatalf("foreign label should 409, got %d %v", code, out)
	}
}
