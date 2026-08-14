package main

import (
	"context"
	"os/exec"
	"strings"
)

// tailscaleChecks appends tailnet findings to the doctor report:
//
//  1. The TLS-termination trap: a `tailscale serve` HTTPS handler on :443 in
//     front of gerry's proxy terminates TLS with the machine's ts.net
//     certificate, so any custom hostname's SNI dies with a TLS alert
//     before gerry ever sees it. The fix is raw TCP passthrough.
//  2. Missing split DNS: the daemon advertises a tailnet address for dev
//     zones, but the tailnet has no split-DNS route delivering those zones
//     to this machine — peers (phones included) can't resolve the
//     hostnames, which reads as "site can't be reached".
func tailscaleChecks(rep *statusReport) {
	bin := tailscaleBin()
	if bin == "" {
		return // no tailscale on this machine — nothing to check
	}

	// --- serve termination trap -------------------------------------
	if out, err := exec.Command(bin, "serve", "status").Output(); err == nil {
		if serveTerminates443(string(out)) {
			rep.warn("tailscale serve terminates TLS on :443 in front of the proxy — custom hostnames fail their TLS handshake tailnet-side")
			rep.fix("switch to passthrough:  tailscale serve --https=443 off && tailscale serve --bg --tcp=443 tcp://127.0.0.1:443")
		}
	}

	// --- split DNS for advertised zones -----------------------------
	var dnsInfo struct {
		Enabled   bool     `json:"enabled"`
		Zones     []string `json:"zones"`
		Advertise string   `json:"advertise"`
	}
	if err := apiClient().Do(context.Background(), "GET", "/v1/dns", nil, &dnsInfo); err != nil {
		return
	}
	if !dnsInfo.Enabled || dnsInfo.Advertise == "" {
		return // loopback-only DNS: tailnet resolution isn't expected
	}
	out, err := exec.Command(bin, "dns", "status").Output()
	if err != nil {
		return
	}
	routes := splitDNSRoutes(string(out))
	for _, z := range dnsInfo.Zones {
		if _, ok := routes[strings.Trim(z, ".")]; !ok {
			rep.warn("dns advertises %s for zone %q but the tailnet has no split-DNS route for it — peers (and phones) cannot resolve these hostnames", dnsInfo.Advertise, z)
			rep.fix("Tailscale admin console → DNS → Add nameserver → Custom → %s → Restrict to domain %q", strings.TrimSuffix(dnsInfo.Advertise, "."), z)
			rep.fix("no console access? use the machine name instead:  tailscale serve --bg --https=8443 http://127.0.0.1:<port>")
		}
	}
}

// splitDNSRoutes parses `tailscale dns status` output into domain → present.
func splitDNSRoutes(out string) map[string]bool {
	routes := map[string]bool{}
	in := false
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Split DNS Routes:") {
			in = true
			continue
		}
		if in {
			if !strings.HasPrefix(trimmed, "- ") {
				if trimmed == "" {
					continue
				}
				break // next section
			}
			fields := strings.Fields(strings.TrimPrefix(trimmed, "- "))
			if len(fields) > 0 {
				routes[strings.Trim(fields[0], ".")] = true
			}
		}
	}
	return routes
}

// serveTerminates443 reports whether an HTTPS (TLS-terminating) serve
// handler on port 443 proxies to a loopback :443 backend — the exact shape
// that swallows custom-SNI handshakes meant for a local TLS proxy. Handlers
// on other ports (8443/10000) and raw TCP passthrough are fine.
func serveTerminates443(out string) bool {
	in443 := false
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "https://") {
			host := strings.Fields(t)[0]
			// no explicit port = 443
			in443 = !strings.Contains(strings.TrimPrefix(host, "https://"), ":")
			continue
		}
		if !strings.HasPrefix(t, "|--") {
			if t != "" {
				in443 = false
			}
			continue
		}
		if in443 && strings.Contains(t, "proxy") &&
			(strings.Contains(t, "127.0.0.1:443") || strings.Contains(t, "localhost:443")) {
			return true
		}
	}
	return false
}
