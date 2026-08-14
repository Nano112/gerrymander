package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// cmdTailnet is the guided setup for tailnet-wide dev hostnames. It probes
// what already works, then walks through whichever of the three routes is
// missing, each with its exact fix — and re-running it verifies the result.
//
//	route 1: machine-name subdomains  (app.<machine>.<tailnet>.ts.net)
//	route 2: split DNS for dev zones  (app.test from every device)
//	route 3: a port on the machine name (real ts.net certificate)
func cmdTailnet(args []string) error {
	bin := tailscaleBin()
	if bin == "" {
		return fmt.Errorf("tailscale is not installed (or not on PATH) — https://tailscale.com/download")
	}
	self, err := tailscaleSelf(bin)
	if err != nil {
		return err
	}
	fmt.Printf("tailnet setup — this machine is %s (%s)\n\n", self.name, self.ip)

	// ---- route 1: machine-name subdomains ---------------------------
	fmt.Println("route 1: subdomains of the machine name")
	fmt.Printf("         https://app.%s\n", self.name)
	if probeSubdomainResolve(self.name) {
		fmt.Println("  ✓ the tailnet resolves machine subdomains (dns-subdomain-resolve is active)")
		fmt.Println("    make gerry authoritative for the subtree, then claim away:")
		fmt.Printf("      gerry zone add --name %s --profile dev\n", self.name)
		fmt.Printf("      gerry claim --zone %s --label app\n", self.name)
	} else {
		fmt.Println("  ✗ not resolving yet. Tailscale shipped this (Jan 2026) behind a node")
		fmt.Println("    capability; grant it in your tailnet policy:")
		fmt.Println("      1. open https://login.tailscale.com/admin/acls")
		fmt.Println("      2. add inside the top-level braces:")
		fmt.Println(`           "nodeAttrs": [`)
		fmt.Println(`               {"target": ["*"], "attr": ["dns-subdomain-resolve"]},`)
		fmt.Println(`           ],`)
		fmt.Println("      3. save, wait a few seconds, run `gerry tailnet` again — the ✗")
		fmt.Println("         flips to ✓ when it lands.")
		fmt.Println("    If the editor rejects the save with:")
		fmt.Println("        `tailnet is not permitted to use the \"dns-subdomain-resolve\" node attribute`")
		fmt.Println("    the rollout has not reached your tailnet yet — Tailscale allowlists this")
		fmt.Println("    server-side (and clients want ~v1.96+). Nothing on your machine fixes it:")
		fmt.Println("    wait, or ask Tailscale support to enable it. Routes 2 and 3 work today.")
	}

	// ---- route 2: split DNS for dev zones ---------------------------
	fmt.Println()
	fmt.Println("route 2: split DNS for dev zones (short names like app.test, every device)")
	dnsInfo := daemonDNSInfo()
	if dnsInfo == nil || !dnsInfo.Enabled || dnsInfo.Advertise == "" {
		fmt.Println("  ✗ the daemon's DNS is not advertising a tailnet address; in the daemon config:")
		fmt.Printf("      dns: { enabled: true, listen: \":53\", zones: [test], advertise: tailscale }\n")
	} else {
		routes := tailscaleSplitRoutes(bin)
		missing := []string{}
		for _, z := range dnsInfo.Zones {
			if !routes[strings.Trim(z, ".")] {
				missing = append(missing, z)
			}
		}
		if len(missing) == 0 {
			fmt.Printf("  ✓ split DNS delivers %s to this machine\n", strings.Join(dnsInfo.Zones, ", "))
		} else {
			fmt.Printf("  ✗ advertising %s, but the tailnet lacks split-DNS routes for: %s\n",
				dnsInfo.Advertise, strings.Join(missing, ", "))
			fmt.Println("      1. open https://login.tailscale.com/admin/dns")
			fmt.Printf("      2. Add nameserver → Custom → %s\n", strings.TrimSuffix(dnsInfo.Advertise, "."))
			fmt.Printf("      3. enable \"Restrict to domain\" and enter: %s\n", strings.Join(missing, " (and) "))
			fmt.Println("      4. save, then `gerry tailnet` again to verify")
		}
	}

	// ---- route 3: a port on the machine name ------------------------
	fmt.Println()
	fmt.Println("route 3: a port on the machine name (real ts.net certificate, no console)")
	if out, err := exec.Command(bin, "serve", "status").Output(); err == nil && strings.Contains(string(out), ":8443") {
		fmt.Printf("  ✓ https://%s:8443 is being served\n", self.name)
	} else {
		fmt.Println("  – available any time, e.g. for a service on sticky port 51005:")
		fmt.Println("      tailscale serve --bg --https=8443 http://127.0.0.1:51005")
		fmt.Println("    scales to ports 443/8443/10000; browsers trust it with no CA install")
	}

	// ---- trust ------------------------------------------------------
	fmt.Println()
	fmt.Println("device trust (routes 1 & 2 use gerry's CA):")
	fmt.Printf("  laptops:  GERRY_API=http://%s:4780 gerry trust\n", self.ip)
	fmt.Printf("  phones:   open http://%s:4780/v1/ca and install the profile (or tap through the warning once)\n", self.ip)
	return nil
}

type tsSelf struct {
	name string // machine FQDN without trailing dot
	ip   string
}

func tailscaleSelf(bin string) (tsSelf, error) {
	out, err := exec.Command(bin, "status", "--json").Output()
	if err != nil {
		return tsSelf{}, fmt.Errorf("tailscale status: %w (logged in?)", err)
	}
	var st struct {
		Self struct {
			DNSName      string   `json:"DNSName"`
			TailscaleIPs []string `json:"TailscaleIPs"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return tsSelf{}, err
	}
	s := tsSelf{name: strings.TrimSuffix(st.Self.DNSName, ".")}
	for _, ip := range st.Self.TailscaleIPs {
		if p := net.ParseIP(ip); p != nil && p.To4() != nil {
			s.ip = ip
			break
		}
	}
	if s.name == "" || s.ip == "" {
		return tsSelf{}, fmt.Errorf("tailscale reports no DNS name / IPv4 for this machine (MagicDNS off?)")
	}
	return s, nil
}

// probeSubdomainResolve asks tailscaled's resolver for a throwaway
// subdomain of the machine name — the empirical test for the
// dns-subdomain-resolve capability, immune to version guessing.
func probeSubdomainResolve(machine string) bool {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn("gerry-probe."+machine), dns.TypeA)
	c := &dns.Client{Timeout: 3 * time.Second}
	r, _, err := c.Exchange(m, "100.100.100.100:53")
	return err == nil && r != nil && r.Rcode == dns.RcodeSuccess && len(r.Answer) > 0
}

type dnsInfoResp struct {
	Enabled   bool     `json:"enabled"`
	Zones     []string `json:"zones"`
	Advertise string   `json:"advertise"`
}

func daemonDNSInfo() *dnsInfoResp {
	var out dnsInfoResp
	if err := apiClient().Do(context.Background(), "GET", "/v1/dns", nil, &out); err != nil {
		return nil
	}
	return &out
}

func tailscaleSplitRoutes(bin string) map[string]bool {
	out, err := exec.Command(bin, "dns", "status").Output()
	if err != nil {
		return map[string]bool{}
	}
	return splitDNSRoutes(string(out))
}
