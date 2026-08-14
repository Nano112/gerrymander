package api

import (
	"fmt"
	"testing"
)

// The whole point of scoped tokens: an owner token can manage its own
// hostnames and can do nothing else.
func TestOwnerTokenScope(t *testing.T) {
	ts := testServer(t, "rootkey")
	root := "rootkey"

	// mint an owner token via the admin API
	resp, body := doJSON(t, "POST", ts.URL+"/v1/tokens", root,
		`{"name":"tenant-a","scope":"owner","owner_ref":"tenant-a-uuid","zones":["olsyn.com"]}`)
	if resp.StatusCode != 201 {
		t.Fatalf("create token: %d %v", resp.StatusCode, body)
	}
	tok := body["plaintext"].(string)

	// an admin-owned allocation the token must not touch
	resp, other := doJSON(t, "POST", ts.URL+"/v1/claims", root,
		`{"zone":"olsyn.com","label":"plumbus-other","kind":"tenant","source":"seed","owner_ref":"someone-else"}`)
	if resp.StatusCode != 201 {
		t.Fatalf("seed claim: %d %v", resp.StatusCode, other)
	}
	otherID := int(other["allocation"].(map[string]any)["id"].(float64))

	// 1. owner claims for itself (owner_ref comes from the token)
	resp, mine := doJSON(t, "POST", ts.URL+"/v1/claims", tok,
		`{"zone":"olsyn.com","label":"plumbus-mine"}`)
	if resp.StatusCode != 201 {
		t.Fatalf("own claim: %d %v", resp.StatusCode, mine)
	}
	alloc := mine["allocation"].(map[string]any)
	if alloc["owner_ref"] != "tenant-a-uuid" || alloc["kind"] != "tenant" {
		t.Fatalf("claim not forced to token identity: %v", alloc)
	}
	mineID := int(alloc["id"].(float64))

	// 2. cannot claim for someone else
	if resp, _ := doJSON(t, "POST", ts.URL+"/v1/claims", tok,
		`{"zone":"olsyn.com","label":"plumbus-x","owner_ref":"someone-else"}`); resp.StatusCode != 403 {
		t.Fatalf("cross-owner claim allowed: %d", resp.StatusCode)
	}

	// 3. list is confined to own allocations
	resp, list := doJSON(t, "GET", ts.URL+"/v1/allocations", tok, "")
	if resp.StatusCode != 200 {
		t.Fatalf("list: %d", resp.StatusCode)
	}
	for _, a := range list["allocations"].([]any) {
		if a.(map[string]any)["owner_ref"] != "tenant-a-uuid" {
			t.Fatalf("owner list leaked foreign allocation: %v", a)
		}
	}

	// 4. cannot touch a foreign allocation, by any verb
	for _, probe := range []struct{ method, path string }{
		{"GET", fmt.Sprintf("/v1/allocations/%d", otherID)},
		{"POST", fmt.Sprintf("/v1/allocations/%d/rename", otherID)},
		{"DELETE", fmt.Sprintf("/v1/allocations/%d", otherID)},
		{"GET", fmt.Sprintf("/v1/allocations/%d/events", otherID)},
	} {
		if resp, _ := doJSON(t, probe.method, ts.URL+probe.path, tok, `{"label":"stolen"}`); resp.StatusCode != 403 {
			t.Fatalf("%s %s: got %d, want 403", probe.method, probe.path, resp.StatusCode)
		}
	}

	// 5. admin surfaces are closed
	for _, probe := range []struct{ method, path, body string }{
		{"POST", "/v1/zones", `{"name":"evil.com"}`},
		{"POST", "/v1/tokens", `{"name":"sneaky"}`},
		{"POST", "/v1/manifest/apply", `{}`},
		{"GET", "/v1/conflicts", ""},
		{"GET", "/v1/ports", ""},
	} {
		if resp, _ := doJSON(t, probe.method, ts.URL+probe.path, tok, probe.body); resp.StatusCode != 403 {
			t.Fatalf("%s %s: got %d, want 403", probe.method, probe.path, resp.StatusCode)
		}
	}

	// 6. own allocation: rename works, audit trail is visible
	if resp, _ := doJSON(t, "POST", ts.URL+fmt.Sprintf("/v1/allocations/%d/rename", mineID), tok,
		`{"label":"plumbus-renamed"}`); resp.StatusCode != 200 {
		t.Fatalf("own rename: %d", resp.StatusCode)
	}
	resp, ev := doJSON(t, "GET", ts.URL+fmt.Sprintf("/v1/allocations/%d/events", mineID), tok, "")
	if resp.StatusCode != 200 || len(ev["events"].([]any)) == 0 {
		t.Fatalf("own events: %d %v", resp.StatusCode, ev)
	}

	// 7. revoked token stops working immediately
	if resp, _ := doJSON(t, "DELETE", ts.URL+"/v1/tokens/tenant-a", root, ""); resp.StatusCode != 204 {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, "GET", ts.URL+"/v1/allocations", tok, ""); resp.StatusCode != 401 {
		t.Fatalf("revoked token still accepted: %d", resp.StatusCode)
	}
}
