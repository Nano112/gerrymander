package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/manifest"
	"github.com/Nano112/gerrymander/internal/service"
	"github.com/Nano112/gerrymander/internal/store"
)

// POST /v1/manifest/apply — server-side `gerry up`. Clients (the vite
// plugin, editors, CI) upload the gerrymander.yaml content; the daemon
// ensures the zone, claims/updates every hostname, grants sticky ports, and
// (by default) prunes manifest-owned allocations whose labels left the
// file. That last part is what makes "rename a domain in the yaml and
// restart" the entire re-assignment workflow.
type manifestApplyRequest struct {
	YAML string `json:"yaml"`
	// Prune releases allocations owned by this manifest's project that are
	// no longer declared. Default true; pass false to only add.
	Prune *bool `json:"prune,omitempty"`
}

type manifestServiceResult struct {
	Hostnames []string `json:"hostnames"`           // exact fqdns
	Wildcards []string `json:"wildcards,omitempty"` // suffixes also matched (from wildcard claims)
	Port      int      `json:"port,omitempty"`      // sticky grant, when the service uses a port pool
	Listen    []int    `json:"listen,omitempty"`    // extra TLS listener ports in play
}

type manifestApplyResponse struct {
	Project  string                           `json:"project"`
	Zone     string                           `json:"zone"`
	Services map[string]manifestServiceResult `json:"services"`
	Claimed  []string                         `json:"claimed"`
	Updated  []string                         `json:"updated"`
	Pruned   []string                         `json:"pruned"`
}

func (s *Server) handleManifestApply(w http.ResponseWriter, r *http.Request) {
	var req manifestApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.YAML) == "" {
		writeErr(w, 400, "invalid", "body must be {\"yaml\": \"<gerrymander.yaml content>\"}")
		return
	}
	m, err := manifest.Parse([]byte(req.YAML), "manifest")
	if err != nil {
		writeErr(w, 400, "invalid", err.Error())
		return
	}
	ctx := r.Context()
	if _, err := s.Store.EnsureZone(ctx, core.Zone{Name: m.Zone, Profile: "dev", WildcardMode: true}); err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}

	ports := map[string]int{} // ownerRef → sticky value
	resolvePort := func(pool, ownerRef string) (int, error) {
		pa, err := s.Ports.Claim(ctx, pool, m.Project, ownerRef)
		if err != nil {
			return 0, err
		}
		ports[ownerRef] = pa.Value
		return pa.Value, nil
	}
	claims, err := m.Claims(resolvePort)
	if err != nil {
		writeErr(w, 400, "invalid", err.Error())
		return
	}

	resp := manifestApplyResponse{
		Project: m.Project, Zone: m.Zone,
		Services: map[string]manifestServiceResult{},
		Claimed:  []string{}, Updated: []string{}, Pruned: []string{},
	}
	keep := map[string]bool{}
	for _, cl := range claims {
		keep[cl.Label] = true
		_, err := s.Alloc.Claim(ctx, service.ClaimRequest{
			Zone: cl.Zone, Label: cl.Label, Kind: core.KindPlatform, Source: core.SourceManifest,
			Project: cl.Project, OwnerRef: cl.OwnerRef, Spec: cl.Spec,
		})
		if err == nil {
			resp.Claimed = append(resp.Claimed, core.FQDN(cl.Label, cl.Zone))
			continue
		}
		var rej *service.ErrClaimRejected
		if !errors.As(err, &rej) || (rej.Reason != "taken" && rej.Reason != "reserved") {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		existing, gerr := s.Store.GetAllocationByLabel(ctx, cl.Zone, cl.Label)
		if gerr != nil {
			writeErr(w, 409, rej.Reason, rej.Message)
			return
		}
		// Update in place only when this manifest plausibly owns it.
		if existing.OwnerRef != cl.OwnerRef && !(existing.Source == core.SourceManifest && existing.Project == m.Project) {
			writeJSON(w, 409, map[string]any{
				"error":   "taken",
				"message": core.FQDN(cl.Label, cl.Zone) + " is owned by " + existing.OwnerRef + " (" + string(existing.Kind) + ")",
			})
			return
		}
		existing.Spec = cl.Spec
		existing.OwnerRef = cl.OwnerRef
		existing.Project = m.Project
		if err := s.Store.UpdateAllocation(ctx, existing); err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		resp.Updated = append(resp.Updated, existing.FQDN)
	}

	if req.Prune == nil || *req.Prune {
		owned, err := s.Store.ListAllocations(ctx, store.AllocFilter{Zone: m.Zone, Project: m.Project, ExcludeReleased: true})
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		for _, a := range owned {
			if a.Source == core.SourceManifest && !keep[a.Label] {
				if err := s.Alloc.Release(ctx, a.ID); err == nil {
					resp.Pruned = append(resp.Pruned, a.FQDN)
				}
			}
		}
	}

	// Per-service summary for clients configuring dev servers.
	for name, svc := range m.Services {
		ownerRef := m.Project + "/" + name
		res := manifestServiceResult{Port: ports[ownerRef]}
		seenListen := map[int]bool{}
		for _, h := range svc.Hostnames {
			hn := strings.ToLower(strings.TrimSpace(h))
			if strings.HasPrefix(hn, "*.") {
				res.Wildcards = append(res.Wildcards, strings.TrimPrefix(hn, "*."))
			} else {
				res.Hostnames = append(res.Hostnames, hn)
			}
		}
		for _, rt := range svc.Routes {
			if rt.Listen != 0 && !seenListen[rt.Listen] {
				seenListen[rt.Listen] = true
				res.Listen = append(res.Listen, rt.Listen)
			}
		}
		for _, l := range svc.Listen {
			if l != 0 && !seenListen[l] {
				seenListen[l] = true
				res.Listen = append(res.Listen, l)
			}
		}
		resp.Services[name] = res
	}
	writeJSON(w, 200, resp)
}
