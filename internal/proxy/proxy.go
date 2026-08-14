// Package proxy is gerrymander's embedded dev reverse proxy: per-SNI leaf
// certificates from a local CA, a store-driven route table, multi-port TLS
// listeners, and hooks for supervised backends.
package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/store"
)

// Starter resolves a supervised backend to a live address, booting the
// process if needed. Implemented by the supervisor; nil disables supervision.
type Starter interface {
	Ensure(ctx context.Context, alloc core.Allocation, b *core.SupervisedBackend) (string, error)
}

// DockerResolver resolves docker backends to host addresses, maintaining
// relay containers as needed. Implemented by dockerrelay.Manager; nil
// disables docker backends.
type DockerResolver interface {
	Ensure(ctx context.Context, d core.DockerBackend) (string, error)
}

// Options configure the proxy.
type Options struct {
	// HTTPAddr serves the redirect listener ("" disables), e.g. ":80".
	HTTPAddr string
	// TLSAddr is the main TLS listener, e.g. ":443".
	TLSAddr string
	// ExtraTLSPorts get their own TLS listeners (dev parity with per-port
	// Vite/HMR routes), e.g. [5173, 5174, 5175, 5176].
	ExtraTLSPorts []int
	// RebuildEvery is the route-table refresh interval (default 2s).
	RebuildEvery time.Duration
	Log          *slog.Logger
}

// Proxy serves traffic for active allocations.
type Proxy struct {
	store   *store.Store
	ca      *CA
	table   *Table
	starter Starter
	docker  DockerResolver
	opts    Options
}

// New wires a proxy. starter may be nil.
func New(st *store.Store, ca *CA, starter Starter, opts Options) *Proxy {
	if opts.RebuildEvery <= 0 {
		opts.RebuildEvery = 2 * time.Second
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Proxy{store: st, ca: ca, table: &Table{}, starter: starter, opts: opts}
}

// SetDockerResolver enables docker relay backends.
func (p *Proxy) SetDockerResolver(d DockerResolver) { p.docker = d }

// RequestRebuild refreshes the route table immediately (called by the API
// after mutations, so a claim routes on the very next request instead of
// after the poll interval). Synchronous on purpose: the rebuild is one
// small query, and returning before it lands would leave a race the caller
// can observe.
func (p *Proxy) RequestRebuild() {
	p.table.Rebuild(context.Background(), p.store)
}

// Table exposes the route table (tests, diagnostics).
func (p *Proxy) Table() *Table { return p.table }

// Run blocks serving all listeners until ctx is cancelled.
func (p *Proxy) Run(ctx context.Context) error {
	if err := p.table.Rebuild(ctx, p.store); err != nil {
		return fmt.Errorf("initial route table: %w", err)
	}
	go p.table.Watch(ctx, p.store, p.opts.RebuildEvery)

	errc := make(chan error, 8)
	var servers []*http.Server

	if p.opts.HTTPAddr != "" {
		srv := &http.Server{Addr: p.opts.HTTPAddr, Handler: http.HandlerFunc(p.redirectHTTPS)}
		servers = append(servers, srv)
		go func() { errc <- srv.ListenAndServe() }()
	}
	tlsPorts := map[string]int{}
	if p.opts.TLSAddr != "" {
		tlsPorts[p.opts.TLSAddr] = 443
	}
	for _, port := range p.opts.ExtraTLSPorts {
		tlsPorts[fmt.Sprintf(":%d", port)] = port
	}
	for addr, port := range tlsPorts {
		port := port
		srv := &http.Server{
			Addr:      addr,
			Handler:   p.handler(port),
			TLSConfig: p.ca.TLSConfig(),
		}
		servers = append(servers, srv)
		go func() { errc <- srv.ListenAndServeTLS("", "") }()
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, srv := range servers {
			srv.Shutdown(shutdownCtx)
		}
		return nil
	case err := <-errc:
		return err
	}
}

func (p *Proxy) redirectHTTPS(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if i := strings.LastIndex(host, ":"); i > -1 {
		host = host[:i]
	}
	http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusPermanentRedirect)
}

func (p *Proxy) handler(listenPort int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if i := strings.LastIndex(host, ":"); i > -1 && !strings.Contains(host[i:], "]") {
			host = host[:i]
		}
		target, ok := p.table.Resolve(r.Host, listenPort)
		if !ok {
			writeUnknownHost(w, r, host, listenPort, p.table.Zones())
			return
		}
		upstream, err := p.upstreamFor(r.Context(), target)
		if err != nil {
			p.opts.Log.Error("upstream", "host", r.Host, "alloc", target.Alloc.FQDN, "err", err)
			writeUpstreamDown(w, r, target, describeBackend(target), err)
			return
		}
		rp := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(upstream)
				// Preserve the original Host: multi-tenant apps route on it.
				pr.Out.Host = pr.In.Host
				pr.SetXForwarded()
				pr.Out.Header.Set("X-Forwarded-Proto", "https")
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				writeUpstreamDown(w, r, target, upstream.String(), err)
			},
			FlushInterval: 100 * time.Millisecond,
		}
		rp.ServeHTTP(w, r)
	})
}

// localBackendHost resolves the "@local" sentinel: dev processes run on the
// machine's host, which a containerized daemon reaches via docker's magic
// name and a host daemon reaches on loopback.
func localBackendHost() string {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "host.docker.internal"
	}
	return "127.0.0.1"
}

// describeBackend renders a backend target for error pages when no URL was
// resolved (e.g. supervision failed before an address existed).
func describeBackend(t Target) string {
	b := t.Backend
	switch {
	case b.Kind == "address" && b.Address != nil:
		return fmt.Sprintf("http://%s:%d", b.Address.Host, b.Address.Port)
	case b.Kind == "supervised" && b.Supervised != nil:
		return "supervised: " + b.Supervised.Cmd
	case b.Kind == "docker" && b.Docker != nil:
		return fmt.Sprintf("docker %s/%s:%d", b.Docker.Network, b.Docker.Host, b.Docker.Port)
	default:
		return b.Kind
	}
}

func (p *Proxy) upstreamFor(ctx context.Context, t Target) (*url.URL, error) {
	b := t.Backend
	switch b.Kind {
	case "address":
		if b.Address == nil {
			return nil, fmt.Errorf("address backend missing address")
		}
		scheme := b.Address.Scheme
		if scheme == "" {
			scheme = "http"
		}
		port := b.Address.Port
		if port == 0 && b.Address.PortPool != "" {
			return nil, fmt.Errorf("port_pool reference %q not resolved at claim time", b.Address.PortPool)
		}
		if port == 0 {
			port = 80
		}
		host := b.Address.Host
		if host == "@local" {
			host = localBackendHost()
		}
		return &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, strconv.Itoa(port))}, nil
	case "supervised":
		if p.starter == nil {
			return nil, fmt.Errorf("supervised backend but supervision is disabled")
		}
		if b.Supervised == nil {
			return nil, fmt.Errorf("supervised backend missing spec")
		}
		addr, err := p.starter.Ensure(ctx, t.Alloc, b.Supervised)
		if err != nil {
			return nil, err
		}
		return &url.URL{Scheme: "http", Host: addr}, nil
	case "docker":
		if p.docker == nil {
			return nil, fmt.Errorf("docker backend but no docker resolver (is the docker CLI available?)")
		}
		if b.Docker == nil {
			return nil, fmt.Errorf("docker backend missing spec")
		}
		addr, err := p.docker.Ensure(ctx, *b.Docker)
		if err != nil {
			return nil, err
		}
		return &url.URL{Scheme: "http", Host: addr}, nil
	case "service":
		return nil, fmt.Errorf("service backends are routed by the cluster ingress, not the embedded proxy")
	default:
		return nil, fmt.Errorf("unknown backend kind %q", b.Kind)
	}
}
