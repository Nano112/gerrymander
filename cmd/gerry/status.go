package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
)

// gerry status — the doctor. Answers "why doesn't my hostname work" in one
// screen: daemon, registry, DNS resolution, proxy TLS, CA trust, each with
// a concrete fix when it fails.

// Color only when talking to a human terminal (and NO_COLOR unset).
var okMark, badMark, warnMark, dimOn, dimOff = func() (string, string, string, string, string) {
	if os.Getenv("NO_COLOR") != "" || !isTTY(os.Stdout) {
		return "ok", "XX", " !", "", ""
	}
	return "\x1b[32m✓\x1b[0m", "\x1b[31m✗\x1b[0m", "\x1b[33m!\x1b[0m", "\x1b[2m", "\x1b[0m"
}()

func isTTY(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

type statusReport struct{ failures int }

func (s *statusReport) ok(f string, a ...any)   { fmt.Printf("  %s %s\n", okMark, fmt.Sprintf(f, a...)) }
func (s *statusReport) warn(f string, a ...any) { fmt.Printf("  %s %s\n", warnMark, fmt.Sprintf(f, a...)) }
func (s *statusReport) bad(f string, a ...any) {
	s.failures++
	fmt.Printf("  %s %s\n", badMark, fmt.Sprintf(f, a...))
}
func (s *statusReport) fix(f string, a ...any) {
	fmt.Printf("      %s→ %s%s\n", dimOn, fmt.Sprintf(f, a...), dimOff)
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	fs.Parse(args)
	rep := &statusReport{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c := apiClient()

	fmt.Printf("\ngerrymander district office — %s\n\n", c.Base)

	// 1. Daemon
	var zones struct {
		Zones []core.Zone `json:"zones"`
	}
	err := c.Do(ctx, "GET", "/v1/zones", nil, &zones)
	switch {
	case err == nil:
		rep.ok("daemon reachable, %d zone(s)", len(zones.Zones))
	default:
		if apiErr, okc := err.(interface{ Error() string }); okc && contains(apiErr.Error(), "unauthorized") {
			rep.bad("daemon requires an API key")
			rep.fix("export GERRY_API_KEY=…  (local daemons usually run keyless)")
		} else {
			rep.bad("daemon unreachable: %v", err)
			rep.fix("local: cd <gerrymander>/deploy/dev && docker compose up -d")
			rep.fix("or point GERRY_API at the right instance (current: %s)", c.Base)
		}
		fmt.Println()
		os.Exit(1)
	}

	// 2. Registry summary
	var allocs struct {
		Allocations []core.Allocation `json:"allocations"`
	}
	if c.Do(ctx, "GET", "/v1/allocations", nil, &allocs) == nil {
		byZone := map[string]int{}
		for _, a := range allocs.Allocations {
			byZone[a.ZoneName]++
		}
		for _, z := range zones.Zones {
			rep.ok("zone %-24s %2d hostname(s)  [%s]", z.Name, byZone[z.Name], z.Profile)
		}
	}
	var ports struct {
		Ports []core.PortAllocation `json:"ports"`
	}
	if c.Do(ctx, "GET", "/v1/ports", nil, &ports) == nil {
		rep.ok("%d sticky port grant(s)", len(ports.Ports))
	}

	fmt.Println()

	// Interference: a system-level HTTP proxy (Proxyman, Charles, corporate)
	// sits between browsers and the dev proxy; when pages fail only in the
	// browser while curl works, this is almost always why.
	if p := systemProxy(); p != "" {
		rep.warn("system HTTP proxy active: %s — browser traffic to dev hosts flows through it", p)
		rep.fix("if pages fail only in the browser, quit/pause the proxy app (its stale sockets outlive daemon restarts)")
	}

	// 3. DNS + proxy + trust per dev zone (probe one representative host).
	probed := false
	for _, z := range zones.Zones {
		if z.Profile != "dev" {
			continue
		}
		probed = true
		probeZone(rep, z.Name)
	}
	if !probed {
		rep.warn("no dev-profile zones — skipping DNS/proxy/trust probes")
	}

	fmt.Println()
	if rep.failures == 0 {
		fmt.Println("  all districts in order.")
	} else {
		fmt.Printf("  %d problem(s) found.\n", rep.failures)
	}
	fmt.Println()
	if rep.failures > 0 {
		os.Exit(1)
	}
	return nil
}

func probeZone(rep *statusReport, zone string) {
	host := "gerry-probe." + zone

	// DNS: does the wildcard resolve? (Uses the system resolver, which on
	// macOS honours /etc/resolver/<tld>.)
	addrs, err := net.DefaultResolver.LookupHost(context.Background(), host)
	if err != nil || len(addrs) == 0 {
		rep.bad("%-26s DNS: %s does not resolve", zone, host)
		if runtime.GOOS == "darwin" {
			rep.fix("dnsmasq: address=/.%s/127.0.0.1 + /etc/resolver/%s → nameserver 127.0.0.1", tld(zone), tld(zone))
		} else {
			rep.fix("dnsmasq: address=/.%s/127.0.0.1 (and point resolv.conf/systemd-resolved at it)", tld(zone))
		}
		rep.fix("or enable gerry's embedded DNS in the daemon config")
		return
	}
	loop := addrs[0] == "127.0.0.1" || addrs[0] == "::1"
	if !loop {
		rep.warn("%-26s DNS resolves to %s (expected loopback)", zone, addrs[0])
	} else {
		rep.ok("%-26s DNS → %s", zone, addrs[0])
	}

	// Proxy + TLS: handshake with SNI, then once more with system roots to
	// judge CA trust.
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 4 * time.Second}, "tcp", "127.0.0.1:443", &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
	})
	if err != nil {
		rep.bad("%-26s proxy: no TLS listener on 127.0.0.1:443 (%v)", zone, err)
		if h := portHolder(":443"); h != "unknown holder" {
			rep.fix("port 443 is %s — stop it or move gerry's proxy.tls in the config", h)
		} else {
			rep.fix("is the gerry daemon's proxy enabled and port 443 published?")
		}
		return
	}
	leaf := conn.ConnectionState().PeerCertificates[0]
	issuer := leaf.Issuer.CommonName
	conn.Close()
	rep.ok("%-26s proxy: TLS listener minting for *.%s (issuer: %s)", zone, zone, issuer)

	roots, _ := x509.SystemCertPool()
	if roots != nil {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host}); err != nil {
			rep.warn("%-26s trust: browsers will warn — CA %q is not trusted by the system", zone, issuer)
			if runtime.GOOS == "darwin" {
				rep.fix("gerry ca-export > /tmp/gerry-ca.pem && sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain /tmp/gerry-ca.pem")
			} else {
				rep.fix("gerry ca-export | sudo tee /usr/local/share/ca-certificates/gerry.crt && sudo update-ca-certificates   # Debian/Ubuntu")
				rep.fix("or: gerry ca-export > gerry.pem && sudo trust anchor gerry.pem   # Fedora/Arch (p11-kit)")
			}
		} else {
			rep.ok("%-26s trust: certificate chain verifies against the system trust store", zone)
		}
	}
}

// systemProxy reports an active system/env HTTP proxy, or "".
func systemProxy() string {
	for _, v := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if p := os.Getenv(v); p != "" {
			return p + " (env " + v + ")"
		}
	}
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("scutil", "--proxy").Output()
		if err == nil {
			s := string(out)
			if strings.Contains(s, "HTTPSEnable : 1") || strings.Contains(s, "HTTPEnable : 1") {
				host, port := scutilVal(s, "HTTPSProxy"), scutilVal(s, "HTTPSPort")
				if host == "" {
					host, port = scutilVal(s, "HTTPProxy"), scutilVal(s, "HTTPPort")
				}
				return host + ":" + port + " (system settings)"
			}
		}
	}
	return ""
}

func scutilVal(s, key string) string {
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, key+" : "); i > -1 {
			return strings.TrimSpace(line[i+len(key)+3:])
		}
	}
	return ""
}

func tld(zone string) string {
	for i := len(zone) - 1; i >= 0; i-- {
		if zone[i] == '.' {
			return zone[i+1:]
		}
	}
	return zone
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
