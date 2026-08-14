// Package observe watches what is actually routed (Traefik IngressRoutes,
// k8s Ingresses), imports platform hostnames into the registry, detects
// conflicts, and runs the catch-all shadow check.
package observe

import (
	"regexp"
	"strings"
)

// HostPattern is one hostname requirement extracted from a route match.
type HostPattern struct {
	Host     string // exact host, if Exact
	Suffix   string // zone suffix for single-label wildcard regexps
	Exact    bool
	CatchAll bool // HostRegexp of the form ^[chars]+\.suffix$
	Raw      string
}

var (
	reHost       = regexp.MustCompile("(?:Host|HostSNI)\\(([^)]*)\\)")
	reHostRegexp = regexp.MustCompile("HostRegexp\\(`([^`]*)`\\)")
	reBacktick   = regexp.MustCompile("`([^`]*)`")
	// The overwhelmingly common catch-all shape: ^[class]+\.suffix$
	reCatchAll = regexp.MustCompile(`^\^\[[^\]]+\][+*]\\\.(.+)\$$`)
)

// ParseMatch extracts host patterns from a Traefik match rule. Path/method
// clauses are ignored — only host constraints matter for ownership.
func ParseMatch(match string) []HostPattern {
	var out []HostPattern
	for _, m := range reHost.FindAllStringSubmatch(match, -1) {
		for _, h := range reBacktick.FindAllStringSubmatch(m[1], -1) {
			out = append(out, HostPattern{Host: strings.ToLower(h[1]), Exact: true, Raw: match})
		}
	}
	for _, m := range reHostRegexp.FindAllStringSubmatch(match, -1) {
		raw := m[1]
		if ca := reCatchAll.FindStringSubmatch(raw); ca != nil {
			suffix := strings.ToLower(strings.ReplaceAll(ca[1], `\.`, "."))
			out = append(out, HostPattern{Suffix: suffix, CatchAll: true, Raw: match})
		} else {
			out = append(out, HostPattern{Raw: match}) // unrecognized regexp; kept for visibility
		}
	}
	return out
}

// ObservedRoute is one route as seen in the cluster.
type ObservedRoute struct {
	Namespace string
	Name      string
	Kind      string // IngressRoute | Ingress
	Match     string
	Priority  int // explicit; 0 = default
	Hosts     []HostPattern
	Service   string // "ns/name:port" of the first backend, informational
	// Managed marks routes the gerry actuator wrote itself
	// (app.gerrymander/managed=true). The observer must not classify its
	// own output: a managed route for a tenant allocation is the
	// materialization of that allocation, not a squatter.
	Managed bool
}

// EffectivePriority mirrors Traefik: explicit priority, else rule length.
func (r ObservedRoute) EffectivePriority() int {
	if r.Priority != 0 {
		return r.Priority
	}
	return len(r.Match)
}

// Conflict is a finding the observer reports and never auto-resolves.
type Conflict struct {
	Zone    string `json:"zone"`
	Type    string `json:"type"` // shadowed-host | kind-mismatch | orphan-route | duplicate-route
	Label   string `json:"label,omitempty"`
	Detail  string `json:"detail"`
	Route   string `json:"route,omitempty"` // ns/name
	Related string `json:"related,omitempty"`
}

// ShadowCheck enforces the catch-all invariant for one zone: every bare-Host
// route under the zone must out-prioritize any single-label catch-all over
// the zone root. Ties lose traffic non-deterministically — that is the trap
// this check exists for.
func ShadowCheck(zone string, routes []ObservedRoute) []Conflict {
	var catchAlls []ObservedRoute
	for _, r := range routes {
		for _, h := range r.Hosts {
			if h.CatchAll && h.Suffix == zone {
				catchAlls = append(catchAlls, r)
			}
		}
	}
	if len(catchAlls) == 0 {
		return nil
	}
	var out []Conflict
	for _, ca := range catchAlls {
		for _, r := range routes {
			if r.Namespace == ca.Namespace && r.Name == ca.Name {
				continue
			}
			for _, h := range r.Hosts {
				if !h.Exact || !strings.HasSuffix(h.Host, "."+zone) {
					continue
				}
				// Only single-label hosts fall inside the catch-all's class.
				label := strings.TrimSuffix(h.Host, "."+zone)
				if strings.Contains(label, ".") {
					continue
				}
				if r.EffectivePriority() <= ca.EffectivePriority() {
					out = append(out, Conflict{
						Zone: zone, Type: "shadowed-host", Label: label,
						Detail: sprintfShadow(h.Host, r, ca),
						Route:  r.Namespace + "/" + r.Name, Related: ca.Namespace + "/" + ca.Name,
					})
				}
			}
		}
	}
	return out
}

func sprintfShadow(host string, r, ca ObservedRoute) string {
	return "route for " + host + " has effective priority " + itoa(r.EffectivePriority()) +
		" which does not beat catch-all " + ca.Namespace + "/" + ca.Name +
		" at " + itoa(ca.EffectivePriority()) + "; raise the host route's priority"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		b[n] = '-'
	}
	return string(b[n:])
}

// LabelForZone derives the registry label for an exact host inside zone.
// Returns "", false when the host is not under the zone.
func LabelForZone(host, zone string) (string, bool) {
	host = strings.ToLower(host)
	if host == zone {
		return "@", true
	}
	if strings.HasSuffix(host, "."+zone) {
		return strings.TrimSuffix(host, "."+zone), true
	}
	return "", false
}
