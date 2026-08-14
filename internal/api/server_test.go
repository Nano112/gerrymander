package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/service"
	"github.com/Nano112/gerrymander/internal/store"
)

func testServer(t *testing.T, apiKey string) *httptest.Server {
	t.Helper()
	st, err := store.Open("sqlite:" + filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ports := service.NewPorts(st)
	ports.SkipBindTest = true
	alloc := service.NewAlloc(st, ports)
	ctx := t.Context()
	st.EnsureZone(ctx, core.Zone{Name: "olsyn.com"})
	ports.EnsureDefaultPool(ctx)
	srv := &Server{Store: st, Alloc: alloc, Ports: ports, APIKey: apiKey}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func doJSON(t *testing.T, method, url, key, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return resp, m
}

func TestAuthRequired(t *testing.T) {
	ts := testServer(t, "sekret")
	resp, _ := doJSON(t, "GET", ts.URL+"/v1/zones", "", "")
	if resp.StatusCode != 401 {
		t.Fatalf("no key: want 401, got %d", resp.StatusCode)
	}
	resp, _ = doJSON(t, "GET", ts.URL+"/v1/zones", "wrong", "")
	if resp.StatusCode != 401 {
		t.Fatalf("wrong key: want 401, got %d", resp.StatusCode)
	}
	resp, _ = doJSON(t, "GET", ts.URL+"/v1/zones", "sekret", "")
	if resp.StatusCode != 200 {
		t.Fatalf("right key: want 200, got %d", resp.StatusCode)
	}
	// health endpoints stay open
	r2, err := http.Get(ts.URL + "/healthz")
	if err != nil || r2.StatusCode != 200 {
		t.Fatalf("healthz: %v %d", err, r2.StatusCode)
	}
}

func TestClaimFlow(t *testing.T) {
	ts := testServer(t, "")

	// claim
	resp, body := doJSON(t, "POST", ts.URL+"/v1/claims", "", `{"zone":"olsyn.com","label":"acme","owner_ref":"t1"}`)
	if resp.StatusCode != 201 {
		t.Fatalf("claim: %d %v", resp.StatusCode, body)
	}

	// duplicate → 409 with reason + suggestions
	resp, body = doJSON(t, "POST", ts.URL+"/v1/claims", "", `{"zone":"olsyn.com","label":"acme"}`)
	if resp.StatusCode != 409 || body["error"] != "taken" {
		t.Fatalf("dup claim: %d %v", resp.StatusCode, body)
	}
	if _, ok := body["suggestions"]; !ok {
		t.Error("409 should carry suggestions")
	}

	// blocked label → 409 blocked
	resp, body = doJSON(t, "POST", ts.URL+"/v1/claims", "", `{"zone":"olsyn.com","label":"grafana"}`)
	if resp.StatusCode != 409 || body["error"] != "blocked" {
		t.Fatalf("blocked claim: %d %v", resp.StatusCode, body)
	}

	// availability
	resp, body = doJSON(t, "GET", ts.URL+"/v1/zones/olsyn.com/availability?label=acme", "", "")
	if resp.StatusCode != 200 || body["available"] != false || body["reason"] != "taken" {
		t.Fatalf("availability: %d %v", resp.StatusCode, body)
	}
}

func TestIdempotencyReplay(t *testing.T) {
	ts := testServer(t, "")
	req := func() (*http.Response, map[string]any) {
		r, _ := http.NewRequest("POST", ts.URL+"/v1/claims", strings.NewReader(`{"zone":"olsyn.com","label":"idem"}`))
		r.Header.Set("Idempotency-Key", "abc-123")
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		return resp, m
	}
	r1, b1 := req()
	if r1.StatusCode != 201 {
		t.Fatalf("first: %d", r1.StatusCode)
	}
	r2, b2 := req()
	if r2.StatusCode != 201 || r2.Header.Get("Idempotency-Replay") != "true" {
		t.Fatalf("replay: %d hdr=%q", r2.StatusCode, r2.Header.Get("Idempotency-Replay"))
	}
	a1, _ := json.Marshal(b1["allocation"])
	a2, _ := json.Marshal(b2["allocation"])
	if string(a1) != string(a2) {
		t.Fatalf("replay differs: %s vs %s", a1, a2)
	}
}

func TestPortsAPI(t *testing.T) {
	ts := testServer(t, "")
	resp, body := doJSON(t, "POST", ts.URL+"/v1/ports", "", `{"owner_ref":"proj-a"}`)
	if resp.StatusCode != 201 {
		t.Fatalf("port claim: %d %v", resp.StatusCode, body)
	}
	v1 := body["value"].(float64)
	// sticky
	_, body = doJSON(t, "POST", ts.URL+"/v1/ports", "", `{"owner_ref":"proj-a"}`)
	if body["value"].(float64) != v1 {
		t.Fatalf("stickiness: %v vs %v", v1, body["value"])
	}
	resp, body = doJSON(t, "GET", ts.URL+"/v1/ports?pool=dev", "", "")
	if resp.StatusCode != 200 || len(body["ports"].([]any)) != 1 {
		t.Fatalf("list ports: %d %v", resp.StatusCode, body)
	}
}

func TestReleaseEndpoint(t *testing.T) {
	ts := testServer(t, "")
	_, body := doJSON(t, "POST", ts.URL+"/v1/claims", "", `{"zone":"olsyn.com","label":"gone"}`)
	id := int64(body["allocation"].(map[string]any)["id"].(float64))
	req, _ := http.NewRequest("DELETE", ts.URL+"/v1/allocations/"+jsonNum(id), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 204 {
		t.Fatalf("release: %v %d", err, resp.StatusCode)
	}
	r2, av := doJSON(t, "GET", ts.URL+"/v1/zones/olsyn.com/availability?label=gone", "", "")
	if r2.StatusCode != 200 || av["available"] != true {
		t.Fatalf("after release: %v", av)
	}
}

func jsonNum(i int64) string {
	b, _ := json.Marshal(i)
	return string(b)
}
