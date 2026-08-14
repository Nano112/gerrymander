// Package service implements gerrymander's application logic on top of the
// store: claims, holds, availability, sticky ports, and the hold reaper.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/store"
)

// Clock is injectable for tests.
type Clock func() time.Time

// Alloc is the allocation service.
type Alloc struct {
	Store  *store.Store
	Ports  *Ports
	Now    Clock
	// Policies by name; zones reference a policy by name, falling back to
	// "default".
	Policies map[string]*core.Policy
	// DefaultHoldTTL is applied when a hold is requested without a TTL.
	DefaultHoldTTL time.Duration
}

// NewAlloc wires an allocation service with the default policy.
func NewAlloc(st *store.Store, ports *Ports) *Alloc {
	return &Alloc{
		Store: st,
		Ports: ports,
		Now:   time.Now,
		Policies: map[string]*core.Policy{
			"default": core.DefaultTenantPolicy(),
		},
		DefaultHoldTTL: 15 * time.Minute,
	}
}

func (a *Alloc) policyFor(z core.Zone) *core.Policy {
	if p, ok := a.Policies[z.PolicyName]; ok {
		return p
	}
	return a.Policies["default"]
}

// Availability describes whether a label can be claimed and why not.
type Availability struct {
	Zone        string   `json:"zone"`
	Label       string   `json:"label"`
	Available   bool     `json:"available"`
	Reason      string   `json:"reason,omitempty"` // taken | reserved | blocked | policy_violation | invalid
	Message     string   `json:"message,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
}

// CheckAvailability answers "can a tenant claim this label here?".
func (a *Alloc) CheckAvailability(ctx context.Context, zoneName, rawLabel string) (Availability, error) {
	out := Availability{Zone: zoneName, Label: rawLabel}
	zone, err := a.Store.GetZone(ctx, zoneName)
	if err != nil {
		return out, err
	}
	label, err := core.Normalize(rawLabel, core.KindTenant)
	if err != nil {
		out.Reason, out.Message = "invalid", err.Error()
		return out, nil
	}
	out.Label = label
	if res := a.policyFor(zone).Check(label, core.KindTenant); res.Reason != "" {
		out.Reason, out.Message = res.Reason, res.Message
		out.Suggestions = a.filterAvailable(ctx, zone, core.Suggest(label))
		return out, nil
	}
	existing, err := a.Store.GetAllocationByLabel(ctx, zoneName, label)
	if err == nil && existing.State != core.StateReleased {
		out.Reason = "taken"
		if existing.Kind != core.KindTenant {
			out.Reason = "reserved"
		}
		out.Message = fmt.Sprintf("%q is %s", label, out.Reason)
		out.Suggestions = a.filterAvailable(ctx, zone, core.Suggest(label))
		return out, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return out, err
	}
	out.Available = true
	return out, nil
}

func (a *Alloc) filterAvailable(ctx context.Context, zone core.Zone, candidates []string) []string {
	var out []string
	for _, c := range candidates {
		norm, err := core.Normalize(c, core.KindTenant)
		if err != nil || a.policyFor(zone).Check(norm, core.KindTenant).Reason != "" {
			continue
		}
		if _, err := a.Store.GetAllocationByLabel(ctx, zone.Name, norm); errors.Is(err, store.ErrNotFound) {
			out = append(out, norm)
		}
		if len(out) >= 3 {
			break
		}
	}
	return out
}

// ClaimRequest asks for a hostname, a port, or both in one transaction-ish
// operation (hostname first; port grant is idempotent-sticky so a retry after
// partial failure converges).
type ClaimRequest struct {
	Zone      string            `json:"zone,omitempty"`
	Label     string            `json:"label,omitempty"`
	Kind      core.Kind         `json:"kind,omitempty"`
	Source    core.Source       `json:"source,omitempty"`
	Project   string            `json:"project,omitempty"`
	OwnerRef  string            `json:"owner_ref,omitempty"`
	OwnerKind string            `json:"owner_kind,omitempty"`
	Spec      core.Spec         `json:"spec,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	// Hold requests a pending reservation instead of an active claim.
	Hold    bool          `json:"hold,omitempty"`
	HoldTTL core.Duration `json:"hold_ttl,omitempty"`
	// PortPool, when set, also grants a sticky port for OwnerRef.
	PortPool string `json:"port_pool,omitempty"`
}

// ClaimResponse is what a successful claim returns.
type ClaimResponse struct {
	Allocation *core.Allocation     `json:"allocation,omitempty"`
	Port       *core.PortAllocation `json:"port,omitempty"`
}

// ErrClaimRejected wraps a policy/availability rejection with its reason.
type ErrClaimRejected struct {
	Reason      string
	Message     string
	Suggestions []string
}

func (e *ErrClaimRejected) Error() string { return e.Reason + ": " + e.Message }

// Claim performs the claim. Hostname conflicts return *ErrClaimRejected with
// reason "taken"/"reserved"/"blocked"/"policy_violation"/"invalid".
func (a *Alloc) Claim(ctx context.Context, req ClaimRequest) (ClaimResponse, error) {
	var resp ClaimResponse
	if req.Zone != "" && req.Label != "" {
		alloc, err := a.claimHostname(ctx, req)
		if err != nil {
			return resp, err
		}
		resp.Allocation = alloc
	}
	if req.PortPool != "" {
		if req.OwnerRef == "" {
			return resp, &ErrClaimRejected{Reason: "invalid", Message: "port claims require owner_ref"}
		}
		port, err := a.Ports.Claim(ctx, req.PortPool, req.Project, req.OwnerRef)
		if err != nil {
			return resp, err
		}
		resp.Port = &port
	}
	if resp.Allocation == nil && resp.Port == nil {
		return resp, &ErrClaimRejected{Reason: "invalid", Message: "nothing requested: provide zone+label and/or port_pool"}
	}
	return resp, nil
}

func (a *Alloc) claimHostname(ctx context.Context, req ClaimRequest) (*core.Allocation, error) {
	zone, err := a.Store.GetZone(ctx, req.Zone)
	if err != nil {
		return nil, err
	}
	// Kind defaults by zone profile: dev zones are single-operator, so an
	// unqualified claim is infrastructure (platform — blocklist doesn't
	// apply; it exists to police *tenant signups*). Prod zones keep the
	// strict tenant default.
	kind := req.Kind
	if kind == "" {
		if zone.Profile == "dev" {
			kind = core.KindPlatform
		} else {
			kind = core.KindTenant
		}
	}
	label, err := core.Normalize(req.Label, kind)
	if err != nil {
		return nil, &ErrClaimRejected{Reason: "invalid", Message: err.Error()}
	}
	// Policy gates the signup path. Trusted sources (seed backfills of
	// pre-existing tenants, manifests, GitOps, the observer) bypass it —
	// grandfathering an existing tenant named "test" must not fail on the
	// blocklist that exists to stop NEW signups taking "test".
	source := orSource(req.Source, core.SourceAPI)
	if source == core.SourceAPI {
		if res := a.policyFor(zone).Check(label, kind); res.Reason != "" {
			return nil, &ErrClaimRejected{Reason: res.Reason, Message: res.Message, Suggestions: a.filterAvailable(ctx, zone, core.Suggest(label))}
		}
	}
	alloc := core.Allocation{
		ZoneID: zone.ID, ZoneName: zone.Name, Project: req.Project,
		Label: label, FQDN: core.FQDN(label, zone.Name),
		Kind: kind, Source: source,
		OwnerRef: req.OwnerRef, OwnerKind: req.OwnerKind,
		Spec: req.Spec, Labels: req.Labels,
		State: core.StateActive,
	}
	if req.Hold {
		ttl := req.HoldTTL.Std()
		if ttl <= 0 {
			ttl = a.DefaultHoldTTL
		}
		exp := a.Now().Add(ttl).UTC()
		alloc.State = core.StatePending
		alloc.ExpiresAt = &exp
	}
	alloc.Status.SetCondition(core.ConditionStatus{Type: core.CondAccepted, Status: true, At: a.Now()})
	created, err := a.Store.CreateAllocation(ctx, alloc)
	if errors.Is(err, store.ErrTaken) {
		// Distinguish taken-by-tenant from reserved-by-platform for the caller.
		reason := "taken"
		if existing, gerr := a.Store.GetAllocationByLabel(ctx, zone.Name, label); gerr == nil && existing.Kind != core.KindTenant {
			reason = "reserved"
		}
		return nil, &ErrClaimRejected{Reason: reason, Message: fmt.Sprintf("%q is %s in %s", label, reason, zone.Name), Suggestions: a.filterAvailable(ctx, zone, core.Suggest(label))}
	}
	if err != nil {
		return nil, err
	}
	created.ZoneName = zone.Name
	return &created, nil
}

// Commit moves a pending hold to active and clears its expiry.
func (a *Alloc) Commit(ctx context.Context, id int64) (core.Allocation, error) {
	al, err := a.Store.GetAllocation(ctx, id)
	if err != nil {
		return al, err
	}
	if al.State != core.StatePending {
		return al, fmt.Errorf("allocation %d is %s, not pending", id, al.State)
	}
	al.State = core.StateActive
	al.ExpiresAt = nil
	return al, a.Store.UpdateAllocation(ctx, al)
}

// Renew extends a hold's expiry.
func (a *Alloc) Renew(ctx context.Context, id int64, ttl time.Duration) (core.Allocation, error) {
	al, err := a.Store.GetAllocation(ctx, id)
	if err != nil {
		return al, err
	}
	if al.State != core.StatePending || al.ExpiresAt == nil {
		return al, fmt.Errorf("allocation %d is not a hold", id)
	}
	if ttl <= 0 {
		ttl = a.DefaultHoldTTL
	}
	exp := a.Now().Add(ttl).UTC()
	al.ExpiresAt = &exp
	return al, a.Store.UpdateAllocation(ctx, al)
}

// Rename atomically moves an allocation to a new label in its zone. The new
// label passes normalization and (for tenants) policy; conflicts surface as
// *ErrClaimRejected exactly like a claim. Everything else about the
// allocation — ID, owner, routes, conditions, event history — survives.
//
// Scope note: this renames the REGISTRY entry only. Systems that store the
// hostname themselves (an app's domains table, TLS SANs, bookmarks) are the
// caller's responsibility.
func (a *Alloc) Rename(ctx context.Context, id int64, rawLabel string) (core.Allocation, error) {
	al, err := a.Store.GetAllocation(ctx, id)
	if err != nil {
		return al, err
	}
	zone, err := a.Store.GetZone(ctx, al.ZoneName)
	if err != nil {
		return al, err
	}
	label, err := core.Normalize(rawLabel, al.Kind)
	if err != nil {
		return al, &ErrClaimRejected{Reason: "invalid", Message: err.Error()}
	}
	if label == al.Label {
		return al, nil // no-op rename
	}
	if res := a.policyFor(zone).Check(label, al.Kind); res.Reason != "" {
		return al, &ErrClaimRejected{Reason: res.Reason, Message: res.Message, Suggestions: a.filterAvailable(ctx, zone, core.Suggest(label))}
	}
	err = a.Store.RenameAllocation(ctx, id, label, core.FQDN(label, zone.Name))
	if errors.Is(err, store.ErrTaken) {
		reason := "taken"
		if existing, gerr := a.Store.GetAllocationByLabel(ctx, zone.Name, label); gerr == nil && existing.Kind != core.KindTenant {
			reason = "reserved"
		}
		return al, &ErrClaimRejected{Reason: reason, Message: fmt.Sprintf("%q is %s in %s", label, reason, zone.Name), Suggestions: a.filterAvailable(ctx, zone, core.Suggest(label))}
	}
	if err != nil {
		return al, err
	}
	return a.Store.GetAllocation(ctx, id)
}

// Release transitions to released. The row stays for audit; the unique index
// ignores nothing, so re-claiming a released label requires deleting the row —
// ReleaseAndFree does both.
func (a *Alloc) Release(ctx context.Context, id int64) error {
	al, err := a.Store.GetAllocation(ctx, id)
	if err != nil {
		return err
	}
	al.State = core.StateReleased
	al.ExpiresAt = nil
	if err := a.Store.UpdateAllocation(ctx, al); err != nil {
		return err
	}
	// Free the label immediately: audit history lives in events.
	return a.Store.DeleteAllocation(ctx, id)
}

// RunReaper expires stale holds until ctx is done.
func (a *Alloc) RunReaper(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.Store.ReapExpiredHolds(ctx, a.Now())
		}
	}
}

func orSource(s, d core.Source) core.Source {
	if s == "" {
		return d
	}
	return s
}
