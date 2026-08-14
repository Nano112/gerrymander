package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/store"
)

func addrBackend(host string, port int) core.Backend {
	return core.Backend{Kind: "address", Address: &core.AddressBackend{Host: host, Port: port}}
}

func seedProxyStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open("sqlite:" + filepath.Join(t.TempDir(), "proxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	z, _ := st.EnsureZone(ctx, core.Zone{Name: "olsyn.test", Profile: "dev"})
	mk := func(label string, spec core.Spec) {
		if _, err := st.CreateAllocation(ctx, core.Allocation{
			ZoneID: z.ID, Label: label, FQDN: core.FQDN(strings.TrimPrefix(label, "*."), "olsyn.test"),
			Kind: core.KindPlatform, Source: core.SourceManifest, State: core.StateActive, Spec: spec,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// apex + wildcard → app:80 on default listener, app:5175 on :5175
	mk("@", core.Spec{Wildcard: true, Routes: []core.Route{
		{Listen: 0, Backend: addrBackend("app-main", 80)},
		{Listen: 5175, Backend: addrBackend("app-vite", 5175)},
	}})
	// exact deep label overrides wildcard
	mk("special", core.Spec{Routes: []core.Route{{Listen: 0, Backend: addrBackend("special-app", 8080)}}})
	return st
}

func TestTableResolution(t *testing.T) {
	st := seedProxyStore(t)
	tbl := &Table{}
	if err := tbl.Rebuild(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		host string
		port int
		want string // backend host, "" = miss
	}{
		{"olsyn.test", 443, "app-main"},
		{"olsyn.test", 80, "app-main"},
		{"gv.olsyn.test", 443, "app-main"},    // wildcard
		{"a.b.olsyn.test", 443, "app-main"},   // deep wildcard match
		{"special.olsyn.test", 443, "special-app"}, // exact beats wildcard
		{"olsyn.test", 5175, "app-vite"},      // port-specific route
		{"gv.olsyn.test", 5175, "app-vite"},   // wildcard + port route
		{"special.olsyn.test", 5174, ""},      // no route for that port
		{"other.test", 443, ""},               // unknown zone
	}
	for _, c := range cases {
		got, ok := tbl.Resolve(c.host, c.port)
		if c.want == "" {
			if ok {
				t.Errorf("Resolve(%s,%d): want miss, got %s", c.host, c.port, got.Backend.Address.Host)
			}
			continue
		}
		if !ok {
			t.Errorf("Resolve(%s,%d): want %s, got miss", c.host, c.port, c.want)
			continue
		}
		if got.Backend.Address.Host != c.want {
			t.Errorf("Resolve(%s,%d) = %s, want %s", c.host, c.port, got.Backend.Address.Host, c.want)
		}
	}
}

func TestPendingHoldsDoNotRoute(t *testing.T) {
	st := seedProxyStore(t)
	ctx := context.Background()
	z, _ := st.GetZone(ctx, "olsyn.test")
	exp := time.Now().Add(time.Hour)
	st.CreateAllocation(ctx, core.Allocation{
		ZoneID: z.ID, Label: "held", FQDN: "held.olsyn.test", Kind: core.KindTenant,
		Source: core.SourceAPI, State: core.StatePending, ExpiresAt: &exp,
		Spec: core.Spec{Routes: []core.Route{{Backend: addrBackend("nope", 80)}}},
	})
	tbl := &Table{}
	tbl.Rebuild(ctx, st)
	// wildcard still routes it to app-main (the catch-all), not the hold
	got, ok := tbl.Resolve("held.olsyn.test", 443)
	if !ok || got.Backend.Address.Host != "app-main" {
		t.Fatalf("held label: got %+v ok=%v", got, ok)
	}
}

func TestLeafMintAndCache(t *testing.T) {
	ca, err := EnsureCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l1, err := ca.Leaf("gv.olsyn.test")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(l1.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.DNSNames[0] != "gv.olsyn.test" {
		t.Fatalf("SAN: %v", leaf.DNSNames)
	}
	l2, _ := ca.Leaf("gv.olsyn.test")
	if l1 != l2 {
		t.Fatal("leaf not cached")
	}
}

// End-to-end: HTTPS request → SNI leaf → route table → backend, Host preserved.
func TestProxyEndToEnd(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "host=%s proto=%s", r.Host, r.Header.Get("X-Forwarded-Proto"))
	}))
	defer backend.Close()
	bu, _ := url.Parse(backend.URL)
	bHost, bPortStr, _ := net.SplitHostPort(bu.Host)
	bPort, _ := strconv.Atoi(bPortStr)

	st, err := store.Open("sqlite:" + filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	z, _ := st.EnsureZone(ctx, core.Zone{Name: "e2e.test", Profile: "dev"})
	st.CreateAllocation(ctx, core.Allocation{
		ZoneID: z.ID, Label: "@", FQDN: "e2e.test", Kind: core.KindPlatform, Source: core.SourceManifest,
		State: core.StateActive,
		Spec:  core.Spec{Wildcard: true, Routes: []core.Route{{Backend: addrBackend(bHost, bPort)}}},
	})

	ca, err := EnsureCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Free port for the TLS listener.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	tlsPort := l.Addr().(*net.TCPAddr).Port
	l.Close()

	p := New(st, ca, nil, Options{TLSAddr: fmt.Sprintf("127.0.0.1:%d", tlsPort)})
	go p.Run(ctx)
	time.Sleep(300 * time.Millisecond)

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.RootPEM())
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "sub.e2e.test"},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tlsPort))
		},
	}}
	resp, err := client.Get(fmt.Sprintf("https://sub.e2e.test:%d/x", tlsPort))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	got := string(body)
	if !strings.Contains(got, "host=sub.e2e.test") {
		t.Errorf("Host not preserved: %q", got)
	}
	if !strings.Contains(got, "proto=https") {
		t.Errorf("X-Forwarded-Proto missing: %q", got)
	}
	// Unknown host → 404 from gerry, not a proxy attempt. Non-browser
	// clients keep the plain-text contract.
	resp2, err := client.Get(fmt.Sprintf("https://unknown.example:%d/", tlsPort))
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != 404 {
		t.Errorf("unknown host: want 404, got %d", resp2.StatusCode)
	}
	if resp2.Header.Get("X-Gerry-Error") == "" {
		t.Error("error responses must carry X-Gerry-Error")
	}
	if strings.Contains(string(b2), "<html") {
		t.Errorf("non-browser client got HTML: %.80s", b2)
	}

	// Browser (Accept: text/html) gets the diagnostic page with hints.
	req3, _ := http.NewRequest("GET", fmt.Sprintf("https://unknown.e2e.test:%d/", tlsPort), nil)
	req3.Header.Set("Accept", "text/html,application/xhtml+xml")
	req3.Host = "unknown.e2e.test"
	// direct exact-host miss requires a host outside the wildcard
	req3.URL.Host = fmt.Sprintf("nope.other:%d", tlsPort)
	req3.Host = "nope.other"
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	b3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if resp3.StatusCode != 404 || !strings.Contains(string(b3), "unclaimed district") || !strings.Contains(string(b3), "gerry ls") {
		t.Errorf("browser 404 page wrong: %d %.120s", resp3.StatusCode, b3)
	}
}

// A routed host whose backend is down: browsers get the auto-recover page,
// HEAD probes see X-Gerry-Error, CLIs get text.
func TestUpstreamDownPage(t *testing.T) {
	st, err := store.Open("sqlite:" + filepath.Join(t.TempDir(), "down.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	z, _ := st.EnsureZone(ctx, core.Zone{Name: "down.test", Profile: "dev"})
	// Point at a port that is certainly closed.
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	deadPort := closed.Addr().(*net.TCPAddr).Port
	closed.Close()
	st.CreateAllocation(ctx, core.Allocation{
		ZoneID: z.ID, Label: "app", FQDN: "app.down.test", Kind: core.KindPlatform,
		Source: core.SourceManifest, State: core.StateActive, OwnerRef: "proj/app",
		Spec: core.Spec{Routes: []core.Route{{Backend: addrBackend("127.0.0.1", deadPort)}}},
	})

	ca, _ := EnsureCA(t.TempDir())
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	tlsPort := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := New(st, ca, nil, Options{TLSAddr: fmt.Sprintf("127.0.0.1:%d", tlsPort)})
	go p.Run(cctx)
	time.Sleep(300 * time.Millisecond)

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.RootPEM())
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "app.down.test"},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tlsPort))
		},
	}}

	req, _ := http.NewRequest("GET", fmt.Sprintf("https://app.down.test:%d/", tlsPort), nil)
	req.Header.Set("Accept", "text/html")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 {
		t.Fatalf("want 502, got %d", resp.StatusCode)
	}
	s := string(body)
	for _, want := range []string{"backend unreachable", "proj/app", "watching for the backend"} {
		if !strings.Contains(s, want) {
			t.Errorf("502 page missing %q", want)
		}
	}
	// HEAD (the recovery probe) sees the error marker with an empty body.
	reqH, _ := http.NewRequest("HEAD", fmt.Sprintf("https://app.down.test:%d/", tlsPort), nil)
	reqH.Header.Set("Accept", "text/html")
	respH, err := client.Do(reqH)
	if err != nil {
		t.Fatal(err)
	}
	respH.Body.Close()
	if respH.Header.Get("X-Gerry-Error") != "upstream" {
		t.Errorf("HEAD marker: %q", respH.Header.Get("X-Gerry-Error"))
	}
}
