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
	// Unknown host → 404 from gerry, not a proxy attempt.
	resp2, err := client.Get(fmt.Sprintf("https://unknown.example:%d/", tlsPort))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 404 {
		t.Errorf("unknown host: want 404, got %d", resp2.StatusCode)
	}
}
