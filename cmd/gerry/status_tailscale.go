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

	rep.fix("guided tailnet setup: gerry tailnet")

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
	out, err := exec.Command(bin, "dns", "status").Output()
	if err != nil {
		return
	}
	routes := splitDNSRoutes(string(out))

	// A split-DNS route for this zone's TLD pointing at another machine is
	// the worst failure in this file, because nothing looks broken: the
	// system resolver has a more specific local route, so curl and `gerry
	// status` reach the local daemon and pass — while browsers, which follow
	// the tailnet's DNS, silently get a different machine's estate. The
	// symptom is a 502 or a stale page for a stack that is demonstrably up.
	if dnsInfo.Enabled && dnsInfo.Advertise == "" {
		self := tailscaleSelfIPs(bin)
		for _, z := range dnsInfo.Zones {
			target, ok := routes[strings.Trim(tld(z), ".")]
			if !ok || target == "" || self[target] {
				continue
			}
			rep.warn("the tailnet routes .%s to %s, not to this machine — browsers resolve these hostnames to that machine's gerry", strings.Trim(tld(z), "."), target)
			rep.fix("point the route here, or remove it so every machine resolves .%s locally: Tailscale admin console → DNS → Split DNS", strings.Trim(tld(z), "."))
			rep.fix("`tailscale dns status` shows the route; curl and this screen will keep passing while browsers do not")
		}
	}

	if !dnsInfo.Enabled || dnsInfo.Advertise == "" {
		return // loopback-only DNS: tailnet resolution isn't expected
	}
	for _, z := range dnsInfo.Zones {
		if _, ok := routes[strings.Trim(z, ".")]; !ok {
			rep.warn("dns advertises %s for zone %q but the tailnet has no split-DNS route for it — peers (and phones) cannot resolve these hostnames", dnsInfo.Advertise, z)
			rep.fix("Tailscale admin console → DNS → Add nameserver → Custom → %s → Restrict to domain %q", strings.TrimSuffix(dnsInfo.Advertise, "."), z)
			rep.fix("no console access? use the machine name instead:  tailscale serve --bg --https=8443 http://127.0.0.1:<port>")
		}
	}
}

// splitDNSRoutes parses `tailscale dns status` output into domain → target.
// The target is what makes a hijacked zone diagnosable, so it is kept even
// though the advertise check only cares whether a route exists.
func splitDNSRoutes(out string) map[string]string {
	routes := map[string]string{}
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
				target := ""
				// "- domain  -> 100.64.0.1"
				for i, f := range fields {
					if f == "->" && i+1 < len(fields) {
						target = fields[i+1]
						break
					}
				}
				routes[strings.Trim(fields[0], ".")] = target
			}
		}
	}
	return routes
}

// tailscaleSelfIPs is this machine's own tailnet addresses, so a split-DNS
// route that already points here is not reported as a hijack.
func tailscaleSelfIPs(bin string) map[string]bool {
	self := map[string]bool{}
	out, err := exec.Command(bin, "ip").Output()
	if err != nil {
		return self
	}
	for _, line := range strings.Fields(string(out)) {
		self[strings.TrimSpace(line)] = true
	}
	return self
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
