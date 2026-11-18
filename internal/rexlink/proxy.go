package rexlink

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A bridge owns one Rex forward to the remote Gerry HTTPS proxy. The remote
// hostname is used for both TLS verification and virtual-host routing.
type bridge struct {
	Host
	transport *http.Transport
	ctx       context.Context
	mu        sync.Mutex
	addr      string
	done      chan struct{}
}

type Router struct {
	bridges []*bridge
	cancel  context.CancelFunc
}

func NewRouter(ctx context.Context, hosts Hosts) (*Router, error) {
	ctx, cancel := context.WithCancel(ctx)
	router := &Router{cancel: cancel}
	for key, h := range hosts {
		pem, err := os.ReadFile(h.CACert)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("Rex host %s: %w", key, err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			cancel()
			return nil, fmt.Errorf("invalid Rex proxy CA for %s", key)
		}
		b := &bridge{Host: h, ctx: ctx}
		b.transport = &http.Transport{
			TLSClientConfig:     &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     time.Minute,
			DialContext:         b.dial,
		}
		router.bridges = append(router.bridges, b)
	}
	return router, nil
}

// Close waits for Rex to release its managed tunnel blocks.
func (rt *Router) Close() {
	rt.cancel()
	for _, b := range rt.bridges {
		b.mu.Lock()
		done := b.done
		b.mu.Unlock()
		if done != nil {
			<-done
		}
	}
}

func (b *bridge) dial(ctx context.Context, network, _ string) (net.Conn, error) {
	addr, err := b.forward()
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, network, addr)
}

func (b *bridge) forward() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ctx.Err(); err != nil {
		return "", err
	}
	if b.addr != "" {
		return b.addr, nil
	}
	bin := b.Rex
	if bin == "" {
		bin = "rex"
	}
	args := []string{"-S", b.Server, "forward", "open", "--no-open", "--bind", "127.0.0.1"}
	if b.Session != "" {
		args = append(args, "--session", b.Session)
	}
	args = append(args, "https://127.0.0.1:"+strconv.Itoa(b.ProxyPort))
	ctx, cancel := context.WithCancel(b.ctx)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 2 * time.Second
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		cancel()
		return "", err
	}
	// The command prints its allocated listener once, then stays attached.
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(out).ReadString('\n')
		ready <- strings.TrimSpace(line)
	}()
	var raw string
	select {
	case raw = <-ready:
	case <-ctx.Done():
	case <-time.After(12 * time.Second):
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") || u.Port() == "" {
		cancel()
		cmd.Wait()
		return "", fmt.Errorf("Rex did not return a loopback forward for %s (check server version and session)", b.Server)
	}
	b.done = make(chan struct{})
	b.addr = net.JoinHostPort("127.0.0.1", u.Port())
	go func() {
		cmd.Wait()
		cancel()
		b.mu.Lock()
		b.addr = ""
		b.transport.CloseIdleConnections()
		close(b.done)
		b.mu.Unlock()
	}()
	return b.addr, nil
}

// Serve handles requests for mapped names from the viewer's loopback interface.
func (rt *Router) Serve(w http.ResponseWriter, r *http.Request, name string) bool {
	for _, b := range rt.bridges {
		if !inZone(name, b.LocalZone) || !validName(name) {
			continue
		}
		peer, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !net.ParseIP(peer).IsLoopback() || r.TLS == nil || !strings.EqualFold(r.TLS.ServerName, name) {
			http.Error(w, "Rex mappings require a local HTTPS connection", http.StatusForbidden)
			return true
		}
		up := &url.URL{Scheme: "https", Host: strings.TrimSuffix(name, b.LocalZone) + b.RemoteZone}
		rp := httputil.ReverseProxy{Transport: b.transport, Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(up)
			pr.Out.Host = up.Host
			pr.SetXForwarded()
		}}
		rp.ServeHTTP(w, r)
		return true
	}
	return false
}
