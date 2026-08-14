// Package crdingest feeds HostnameReservation custom resources into the
// registry — GitOps input for allocations. The CR is input, the database is
// truth: a reservation that loses a race for a taken label reports the
// conflict in logs and stays unfulfilled rather than stealing the name.
//
// Ownership mirrors the other reconcilers: allocations created from CRs
// carry owner_kind "crd", and only those are released when a CR disappears.
package crdingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/k8slite"
	"github.com/Nano112/gerrymander/internal/service"
	"github.com/Nano112/gerrymander/internal/store"
)

// OwnerKind marks allocations the ingester owns end-to-end.
const OwnerKind = "crd"

const crdBase = "/apis/gerrymander.dev/v1alpha1"

type Ingester struct {
	Store    *store.Store
	Alloc    *service.Alloc
	Client   *k8slite.Client
	Interval time.Duration
	Log      *slog.Logger

	warned map[string]bool
}

type reservation struct {
	Metadata struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Zone     string            `json:"zone"`
		Label    string            `json:"label"`
		Kind     string            `json:"kind"`
		OwnerRef string            `json:"ownerRef"`
		Routes   []json.RawMessage `json:"routes"`
		Wildcard bool              `json:"wildcard"`
	} `json:"spec"`
}

func (i *Ingester) Run(ctx context.Context) {
	if i.Interval <= 0 {
		i.Interval = time.Minute
	}
	i.warned = map[string]bool{}
	t := time.NewTicker(i.Interval)
	defer t.Stop()
	for {
		if err := i.sync(ctx); err != nil && ctx.Err() == nil {
			i.logf("sync: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (i *Ingester) sync(ctx context.Context) error {
	var list struct {
		Items []reservation `json:"items"`
	}
	if err := i.Client.Do(ctx, "GET", crdBase+"/hostnamereservations?limit=1000", nil, &list); err != nil {
		return fmt.Errorf("list hostnamereservations: %w", err)
	}

	desired := map[string]bool{} // zone/label
	for _, r := range list.Items {
		key := r.Spec.Zone + "/" + r.Spec.Label
		desired[key] = true

		ownerRef := r.Spec.OwnerRef
		if ownerRef == "" {
			ownerRef = r.Metadata.Namespace + "/" + r.Metadata.Name
		}
		kind := core.Kind(r.Spec.Kind)
		if kind == "" {
			kind = core.KindPlatform
		}
		var spec core.Spec
		spec.Wildcard = r.Spec.Wildcard
		if len(r.Spec.Routes) > 0 {
			raw, _ := json.Marshal(map[string]any{"routes": r.Spec.Routes, "wildcard": r.Spec.Wildcard})
			json.Unmarshal(raw, &spec)
		}

		existing, err := i.Store.GetAllocationByLabel(ctx, r.Spec.Zone, r.Spec.Label)
		if err == nil {
			if existing.OwnerKind != OwnerKind {
				if !i.warned[key] {
					i.warned[key] = true
					i.logf("%s already claimed by %s/%s — reservation unfulfilled", key, existing.OwnerKind, existing.OwnerRef)
				}
				continue
			}
			if existing.OwnerRef != ownerRef || specChanged(existing.Spec, spec) {
				existing.OwnerRef = ownerRef
				existing.Spec = spec
				if uerr := i.Store.UpdateAllocation(ctx, existing); uerr == nil {
					i.logf("updated %s from reservation %s/%s", key, r.Metadata.Namespace, r.Metadata.Name)
				}
			}
			continue
		}

		_, cerr := i.Alloc.Claim(ctx, service.ClaimRequest{
			Zone: r.Spec.Zone, Label: r.Spec.Label, Kind: kind, Source: core.SourceManifest,
			OwnerRef: ownerRef, OwnerKind: OwnerKind, Spec: spec,
		})
		if cerr != nil {
			if !i.warned[key] {
				i.warned[key] = true
				i.logf("claim %s: %v", key, cerr)
			}
			continue
		}
		delete(i.warned, key)
		i.logf("claimed %s from reservation %s/%s", key, r.Metadata.Namespace, r.Metadata.Name)
	}

	// release CRD-owned allocations whose reservation is gone
	ours, err := i.Store.ListAllocations(ctx, store.AllocFilter{ExcludeReleased: true})
	if err != nil {
		return err
	}
	for _, a := range ours {
		if a.OwnerKind != OwnerKind || desired[a.ZoneName+"/"+a.Label] {
			continue
		}
		if err := i.Alloc.Release(ctx, a.ID); err == nil {
			i.logf("released %s (reservation deleted)", a.FQDN)
		}
	}
	return nil
}

func specChanged(a, b core.Spec) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) != string(jb)
}

func (i *Ingester) logf(format string, args ...any) {
	if i.Log != nil {
		i.Log.Info("crd-ingest: " + fmt.Sprintf(format, args...))
	}
}
