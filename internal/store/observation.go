package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
)

// ObservedOwner identifies a currently present Kubernetes route. Presence is
// keyed by zone/label, not the old owner name: routes may move or be replaced.
type ObservedOwner struct {
	Ref, Kind, UID, Service string
}

type ObservationResult struct {
	Missing, Retired int
}

// ReconcileObservations applies a complete successful snapshot to observer-owned
// allocations only. Retirement and its full audit payload commit atomically.
// resetMissing starts a new absence window after startup, a failed scan or a
// prolonged gap. No historical creation/update time is treated as last-seen.
func (s *Store) ReconcileObservations(ctx context.Context, zone string, seen map[string]ObservedOwner, now time.Time, grace time.Duration, retire, resetMissing bool) (ObservationResult, error) {
	var result ObservationResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, s.q(allocCols+" WHERE z.name = ? AND a.source = 'observed' AND a.kind = 'platform' AND a.state = 'active'"), zone)
	if err != nil {
		return result, err
	}
	var allocs []core.Allocation
	for rows.Next() {
		a, err := scanAllocation(rows)
		if err != nil {
			rows.Close()
			return result, err
		}
		allocs = append(allocs, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	now = now.UTC()
	for _, a := range allocs {
		// An allocation explicitly given routing intent is no longer a passive
		// observation. Never remove it just because the observer cannot see it.
		if len(a.Spec.Routes) != 0 || a.ExpiresAt != nil {
			continue
		}
		previousUpdated := a.UpdatedAt
		if a.Status.Observation == nil {
			a.Status.Observation = &core.ObservationStatus{}
		}
		observation := a.Status.Observation
		if owner, present := seen[a.Label]; present {
			observation.LastSeenAt = &now
			observation.MissingSince = nil
			observation.MissingObservations = 0
			observation.OwnerUID = owner.UID
			a.OwnerRef, a.OwnerKind = owner.Ref, owner.Kind
			if a.Labels == nil {
				a.Labels = map[string]string{}
			}
			a.Labels["observed-service"] = owner.Service
		} else {
			if resetMissing || observation.MissingSince == nil || now.Before(*observation.MissingSince) {
				observation.MissingSince = &now
				observation.MissingObservations = 0
			}
			observation.MissingObservations++
			if retire && grace > 0 && observation.MissingObservations >= 3 && now.Sub(*observation.MissingSince) >= grace {
				// Guard against a concurrent API edit/reassignment after our snapshot.
				deleted, err := tx.ExecContext(ctx, s.q(`DELETE FROM allocations WHERE id = ? AND zone_id = ? AND label = ? AND source = 'observed' AND kind = 'platform' AND state = 'active' AND updated_at = ?`), a.ID, a.ZoneID, a.Label, previousUpdated)
				if err != nil {
					return result, err
				}
				n, err := deleted.RowsAffected()
				if err != nil {
					return result, err
				}
				if n > 0 {
					payload, err := json.Marshal(a)
					if err != nil {
						return result, err
					}
					_, err = tx.ExecContext(ctx, s.q(`INSERT INTO events (subject_type, subject_id, actor, action, from_state, to_state, payload, at) VALUES ('allocation', ?, 'kubernetes-observer', 'retire-missing', 'active', 'released', ?, ?)`), a.ID, string(payload), now)
					if err != nil {
						return result, err
					}
					result.Retired++
				}
				continue
			}
			result.Missing++
		}
		status, err := json.Marshal(a.Status)
		if err != nil {
			return result, err
		}
		labels, err := json.Marshal(a.Labels)
		if err != nil {
			return result, err
		}
		_, err = tx.ExecContext(ctx, s.q(`UPDATE allocations SET status = ?, labels = ?, owner_ref = ?, owner_kind = ?, updated_at = ? WHERE id = ? AND zone_id = ? AND label = ? AND source = 'observed' AND kind = 'platform' AND state = 'active' AND updated_at = ?`), string(status), string(labels), a.OwnerRef, a.OwnerKind, now, a.ID, a.ZoneID, a.Label, previousUpdated)
		if err != nil {
			return result, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ObservationResult{}, err
	}
	return result, nil
}
