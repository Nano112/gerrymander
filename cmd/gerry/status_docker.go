package main

import (
	"context"
	"os"
	"strconv"

	"github.com/Nano112/gerrymander/internal/dockerrelay"
	"github.com/Nano112/gerrymander/internal/manifest"
)

// dockerChecks diagnoses the docker-backend path: the daemon this user can
// actually talk to, then — for the manifest in the working directory — the
// network and the container alias each backend names.
//
// The three failures look identical from the browser (502) and have three
// different fixes, which is the whole reason they are separate lines.
//
// It reports nothing at all when no manifest in the working directory uses a
// docker backend: most projects never touch docker, and a status screen that
// lectures them about it is noise.
func dockerChecks(ctx context.Context, rep *statusReport, file string) {
	backends := manifestDockerBackends(file)
	if len(backends) == 0 {
		return
	}

	h := dockerrelay.ProbeDaemon(ctx)
	switch {
	case !h.Installed:
		rep.bad("docker not installed — %d docker-backed service(s) in %s cannot route", len(backends), file)
		rep.fix("%s", h.Fix)
		return
	case h.Denied:
		rep.bad("docker socket refuses this user — gerry cannot create relays")
		rep.fix("%s", h.Fix)
		rep.fix("gerry inherits the group from its own session: restart the daemon after (gerry service restart)")
		return
	case !h.Reachable:
		rep.bad("docker daemon unreachable: %s", h.Err)
		rep.fix("%s", h.Fix)
		return
	}
	rep.ok("docker %-23s reachable, relays available", h.Version)

	for _, b := range backends {
		label := b.svc
		if !dockerrelay.NetworkExists(ctx, b.network) {
			rep.bad("%-28s network %q does not exist", label, b.network)
			rep.fix("docker network create %s   (then restart the stack so it joins)", b.network)
			continue
		}
		name, found := dockerrelay.ResolveOnNetwork(ctx, b.network, b.host)
		if !found {
			rep.bad("%-28s %q is not on network %q", label, b.host, b.network)
			rep.fix("start the container, or check its networks[].aliases in compose")
			continue
		}
		rep.ok("%-28s → %s on %s:%d", label, name, b.network, b.port)
	}
}

type dockerBackendRef struct {
	svc     string
	network string
	host    string
	port    int
}

// manifestDockerBackends returns every docker backend the manifest declares,
// shorthand and per-route alike. A missing or unparseable manifest yields
// nothing: `gerry status` is a diagnosis tool and must never fail on the way
// to telling you what is wrong.
func manifestDockerBackends(file string) []dockerBackendRef {
	if _, err := os.Stat(file); err != nil {
		return nil
	}
	m, err := manifest.Load(file)
	if err != nil {
		return nil
	}
	var out []dockerBackendRef
	for name, svc := range m.Services {
		if d := svc.Docker; d != nil {
			out = append(out, dockerBackendRef{name, d.Network, d.Host, d.Port})
		}
		for _, r := range svc.Routes {
			if d := r.Docker; d != nil {
				label := name
				if r.Listen != 0 {
					label = name + ":" + strconv.Itoa(r.Listen)
				}
				out = append(out, dockerBackendRef{label, d.Network, d.Host, d.Port})
			}
		}
	}
	return out
}
