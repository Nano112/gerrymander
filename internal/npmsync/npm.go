// Package npmsync reconciles registry allocations into Nginx Proxy Manager
// proxy hosts, over NPM's REST API. NPM stays the dataplane and its UI keeps
// working (certificates, access lists, the lot); gerry is the authority on
// which hostnames exist and where they forward.
//
// Safety contract: every proxy host gerry creates carries a marker comment
// in its advanced_config, and only hosts carrying that marker are ever
// updated or deleted. Hosts you made in the NPM UI are invisible to it.
package npmsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/store"
)

// Marker identifies proxy hosts gerry owns. It lives in advanced_config,
// which NPM passes through into the generated server block as-is.
const Marker = "# gerrymander-managed"

type Sync struct {
	Store *store.Store
	Zones []string
	// URL of the NPM admin API, e.g. http://100.71.144.24:81
	URL string
	// Identity/Secret are the NPM admin login; a short-lived token is
	// minted from them and refreshed on 401.
	Identity string
	Secret   string
	// LocalHost replaces the "@local" backend sentinel. For NPM running in
	// a container, the machine's services live at host.docker.internal.
	LocalHost string
	Interval  time.Duration
	Log       *slog.Logger

	mu    sync.Mutex
	token string
	http_ *http.Client
}

type proxyHost struct {
	ID             int64    `json:"id,omitempty"`
	DomainNames    []string `json:"domain_names"`
	ForwardScheme  string   `json:"forward_scheme"`
	ForwardHost    string   `json:"forward_host"`
	ForwardPort    int      `json:"forward_port"`
	AdvancedConfig string   `json:"advanced_config"`
	// NPM rejects creates without these present.
	AccessListID          int64          `json:"access_list_id"`
	CertificateID         int64          `json:"certificate_id"`
	SSLForced             bool           `json:"ssl_forced"`
	CachingEnabled        bool           `json:"caching_enabled"`
	BlockExploits         bool           `json:"block_exploits"`
	AllowWebsocketUpgrade bool           `json:"allow_websocket_upgrade"`
	HTTP2Support          bool           `json:"http2_support"`
	HSTSEnabled           bool           `json:"hsts_enabled"`
	HSTSSubdomains        bool           `json:"hsts_subdomains"`
	Enabled               bool           `json:"enabled"`
	Locations             []any          `json:"locations"`
	Meta                  map[string]any `json:"meta"`
}

func (s *Sync) client() *http.Client {
	if s.http_ == nil {
		s.http_ = &http.Client{Timeout: 20 * time.Second}
	}
	return s.http_
}

// login mints a bearer token from the admin identity.
func (s *Sync) login(ctx context.Context) error {
	body, _ := json.Marshal(map[string]string{"identity": s.Identity, "secret": s.Secret})
	req, err := http.NewRequestWithContext(ctx, "POST", s.URL+"/api/tokens", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("npm login: %s", resp.Status)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	s.mu.Lock()
	s.token = out.Token
	s.mu.Unlock()
	return nil
}

func (s *Sync) do(ctx context.Context, method, path string, body, into any) error {
	s.mu.Lock()
	tok := s.token
	s.mu.Unlock()
	if tok == "" {
		if err := s.login(ctx); err != nil {
			return err
		}
	}
	attempt := func() (*http.Response, error) {
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req, err := http.NewRequestWithContext(ctx, method, s.URL+path, rd)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		req.Header.Set("Authorization", "Bearer "+s.token)
		s.mu.Unlock()
		req.Header.Set("Content-Type", "application/json")
		return s.client().Do(req)
	}
	resp, err := attempt()
	if err != nil {
		return err
	}
	if resp.StatusCode == 401 { // token expired: refresh once
		resp.Body.Close()
		if err := s.login(ctx); err != nil {
			return err
		}
		if resp, err = attempt(); err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		msg := resp.Status
		if e.Error.Message != "" {
			msg = e.Error.Message
		}
		return fmt.Errorf("%s %s: %s", method, path, msg)
	}
	if into != nil {
		return json.NewDecoder(resp.Body).Decode(into)
	}
	return nil
}

// desired maps the registry to proxy hosts, keyed by primary domain.
func (s *Sync) desired(ctx context.Context) (map[string]proxyHost, error) {
	want := map[string]proxyHost{}
	for _, zone := range s.Zones {
		allocs, err := s.Store.ListAllocations(ctx, store.AllocFilter{Zone: zone, State: string(core.StateActive)})
		if err != nil {
			return nil, err
		}
		for _, al := range allocs {
			if len(al.Spec.Routes) == 0 {
				continue
			}
			be := al.Spec.Routes[0].Backend
			if be.Kind != "address" || be.Address == nil {
				continue
			}
			host := be.Address.Host
			if host == "@local" {
				host = s.LocalHost
			}
			names := []string{al.FQDN}
			if al.Spec.Wildcard {
				names = append(names, "*."+al.FQDN)
			}
			want[al.FQDN] = proxyHost{
				DomainNames:   names,
				ForwardScheme: "http", ForwardHost: host, ForwardPort: be.Address.Port,
				AdvancedConfig:        Marker + " owner=" + al.OwnerRef,
				AllowWebsocketUpgrade: true,
				Enabled:               true,
				Locations:             []any{},
				Meta:                  map[string]any{},
			}
		}
	}
	return want, nil
}

// Reconcile converges NPM's gerry-marked proxy hosts to the registry.
func (s *Sync) Reconcile(ctx context.Context) error {
	want, err := s.desired(ctx)
	if err != nil {
		return err
	}
	var existing []proxyHost
	if err := s.do(ctx, "GET", "/api/nginx/proxy-hosts", nil, &existing); err != nil {
		return fmt.Errorf("list proxy hosts: %w", err)
	}

	seen := map[string]bool{}
	for _, ex := range existing {
		if !strings.HasPrefix(ex.AdvancedConfig, Marker) {
			continue // a host made in the UI — never touched
		}
		if len(ex.DomainNames) == 0 {
			continue
		}
		key := ex.DomainNames[0]
		seen[key] = true
		w, ok := want[key]
		if !ok {
			if err := s.do(ctx, "DELETE", fmt.Sprintf("/api/nginx/proxy-hosts/%d", ex.ID), nil, nil); err != nil {
				s.logf("delete %s: %v", key, err)
			} else {
				s.logf("removed proxy host %s (allocation released)", key)
			}
			continue
		}
		if hostChanged(ex, w) {
			// Partial PUT: only the fields gerry manages. Certificates and
			// access lists attached in the UI survive untouched.
			if err := s.do(ctx, "PUT", fmt.Sprintf("/api/nginx/proxy-hosts/%d", ex.ID), updateBody(w), nil); err != nil {
				s.logf("update %s: %v", key, err)
			} else {
				s.logf("repaired proxy host %s (drift)", key)
			}
		}
	}
	// stable creation order keeps logs deterministic
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if seen[key] {
			continue
		}
		w := want[key]
		if err := s.do(ctx, "POST", "/api/nginx/proxy-hosts", createBody(w), nil); err != nil {
			s.logf("create %s: %v", key, err)
		} else {
			s.logf("created proxy host %s → %s:%d", key, w.ForwardHost, w.ForwardPort)
		}
	}
	return nil
}

// hostChanged compares only gerry-managed fields. Enabled is deliberately
// excluded: disabling a managed host in the NPM UI is user intent, not
// drift, and must not fight a reconcile loop.
func hostChanged(ex, w proxyHost) bool {
	return ex.ForwardHost != w.ForwardHost || ex.ForwardPort != w.ForwardPort ||
		strings.Join(ex.DomainNames, ",") != strings.Join(w.DomainNames, ",") ||
		ex.AdvancedConfig != w.AdvancedConfig
}

// updateBody carries ONLY the fields gerry manages, so a certificate or
// access list attached to a managed host in the NPM UI is never reset.
func updateBody(w proxyHost) map[string]any {
	return map[string]any{
		"domain_names": w.DomainNames, "forward_scheme": w.ForwardScheme,
		"forward_host": w.ForwardHost, "forward_port": w.ForwardPort,
		"advanced_config": w.AdvancedConfig,
	}
}

// createBody is the full shape NPM requires on POST.
func createBody(w proxyHost) map[string]any {
	b := updateBody(w)
	b["allow_websocket_upgrade"] = true
	b["access_list_id"] = 0
	b["certificate_id"] = 0
	b["ssl_forced"] = false
	b["caching_enabled"] = false
	b["block_exploits"] = false
	b["http2_support"] = false
	b["hsts_enabled"] = false
	b["hsts_subdomains"] = false
	b["locations"] = []any{}
	b["meta"] = map[string]any{}
	return b
}

// Run reconciles on an interval until ctx ends.
func (s *Sync) Run(ctx context.Context) {
	if s.Interval <= 0 {
		s.Interval = 30 * time.Second
	}
	if s.LocalHost == "" {
		s.LocalHost = "host.docker.internal"
	}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		if err := s.Reconcile(ctx); err != nil && ctx.Err() == nil {
			s.logf("reconcile: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Sync) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log.Info("npm-sync: " + fmt.Sprintf(format, args...))
	}
}
