package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Nano112/gerrymander/catalog"
)

var ErrCatalogConflict = errors.New("catalog identity or immutable capability version conflict")

// ApplyCatalog publishes one application's metadata atomically. Omission does
// not delete catalog identities. Route reconciliation has its own lifecycle.
func (s *Store) ApplyCatalog(ctx context.Context, b catalog.Bundle) error {
	return s.applyCatalog(ctx, b, true)
}

// CheckCatalog preflights identities, assets and versions without persisting changes.
func (s *Store) CheckCatalog(ctx context.Context, b catalog.Bundle) error {
	return s.applyCatalog(ctx, b, false)
}

func (s *Store) applyCatalog(ctx context.Context, b catalog.Bundle, commit bool) error {
	if err := b.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Serialize catalog writers across daemon replicas; protects immutable identity/version checks.
	if s.postgres {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(1734701667)"); err != nil {
			return err
		}
	}
	put := func(kind, id, naturalKey string, value any, immutable ...string) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		var old string
		err = tx.QueryRowContext(ctx, s.q("SELECT data FROM catalog_entries WHERE kind=? AND id=?"), kind, id).Scan(&old)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			if kind == "capability" && old != string(data) {
				return fmt.Errorf("%w: publish a new capability version for %s", ErrCatalogConflict, id)
			}
			var before, after map[string]any
			json.Unmarshal([]byte(old), &before)
			json.Unmarshal(data, &after)
			for _, key := range immutable {
				if before[key] != after[key] {
					return fmt.Errorf("%w: %s %s cannot change %s", ErrCatalogConflict, kind, id, key)
				}
			}
		}
		var key any
		if naturalKey != "" {
			key = naturalKey
		}
		_, err = tx.ExecContext(ctx, s.q("INSERT INTO catalog_entries(kind,id,application_id,natural_key,data) VALUES(?,?,?,?,?) ON CONFLICT(kind,id) DO UPDATE SET data=excluded.data,updated_at=CURRENT_TIMESTAMP"), kind, id, b.Application.ID, key, string(data))
		if isUnique(err) {
			return fmt.Errorf("%w: %s identity already exists", ErrCatalogConflict, kind)
		}
		return err
	}
	checkLogo := func(display catalog.Display) error {
		if display.Logo == "" {
			return nil
		}
		var found int
		if err := tx.QueryRowContext(ctx, s.q("SELECT COUNT(*) FROM catalog_assets WHERE id=?"), display.Logo).Scan(&found); err != nil {
			return err
		}
		if found != 1 {
			return fmt.Errorf("logo %s has not been imported", display.Logo)
		}
		return nil
	}
	if err := checkLogo(b.Application.Display); err != nil {
		return err
	}
	if err := put("application", b.Application.ID, "", b.Application, "project"); err != nil {
		return err
	}
	for _, v := range b.Services {
		if err := checkLogo(v.Display); err != nil {
			return err
		}
		if err := put("service", v.ID, v.Key, v, "application_id", "key"); err != nil {
			return err
		}
	}
	for _, v := range b.Capabilities {
		if err := put("capability", v.ServiceID+"/"+v.Version, "", v, "service_id", "version"); err != nil {
			return err
		}
	}
	for _, v := range b.Deployments {
		if v.CapabilityVersion != "" {
			var found int
			if err := tx.QueryRowContext(ctx, s.q("SELECT COUNT(*) FROM catalog_entries WHERE kind='capability' AND id=? AND application_id=?"), v.ServiceID+"/"+v.CapabilityVersion, b.Application.ID).Scan(&found); err != nil {
				return err
			}
			if found != 1 {
				return fmt.Errorf("deployment %s selects an unpublished capability version", v.ID)
			}
		}
		if err := put("deployment", v.ID, "", v, "application_id", "service_id", "environment", "workspace", "zone", "owner_ref"); err != nil {
			return err
		}
	}
	for _, v := range b.Resources {
		if err := checkLogo(v.Display); err != nil {
			return err
		}
		if err := put("resource", v.ID, v.Environment+"/"+v.Workspace+"/"+v.Key, v, "application_id", "key", "environment", "workspace"); err != nil {
			return err
		}
	}
	if !commit {
		return nil
	}
	return tx.Commit()
}

func (s *Store) Catalog(ctx context.Context, application string) (catalog.Snapshot, error) {
	result := catalog.Snapshot{SchemaVersion: catalog.SchemaVersion, Applications: []catalog.Application{}, Services: []catalog.Service{}, Capabilities: []catalog.Capabilities{}, Deployments: []catalog.Deployment{}, Resources: []catalog.Resource{}}
	query := "SELECT kind,data FROM catalog_entries"
	args := []any{}
	if application != "" {
		query += " WHERE application_id=?"
		args = append(args, application)
	}
	query += " ORDER BY kind,id"
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, data string
		if err := rows.Scan(&kind, &data); err != nil {
			return result, err
		}
		switch kind {
		case "application":
			var v catalog.Application
			err = json.Unmarshal([]byte(data), &v)
			result.Applications = append(result.Applications, v)
		case "service":
			var v catalog.Service
			err = json.Unmarshal([]byte(data), &v)
			result.Services = append(result.Services, v)
		case "capability":
			var v catalog.Capabilities
			err = json.Unmarshal([]byte(data), &v)
			result.Capabilities = append(result.Capabilities, v)
		case "deployment":
			var v catalog.Deployment
			err = json.Unmarshal([]byte(data), &v)
			result.Deployments = append(result.Deployments, v)
		case "resource":
			var v catalog.Resource
			err = json.Unmarshal([]byte(data), &v)
			result.Resources = append(result.Resources, v)
		}
		if err != nil {
			return result, err
		}
	}
	return result, rows.Err()
}

func (s *Store) DeactivateCatalogDeployment(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if s.postgres {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(1734701667)"); err != nil {
			return err
		}
	}
	var data string
	if err := tx.QueryRowContext(ctx, s.q("SELECT data FROM catalog_entries WHERE kind='deployment' AND id=?"), id).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var d catalog.Deployment
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		return err
	}
	d.Lifecycle = "inactive"
	raw, _ := json.Marshal(d)
	if _, err := tx.ExecContext(ctx, s.q("UPDATE catalog_entries SET data=?,updated_at=CURRENT_TIMESTAMP WHERE kind='deployment' AND id=?"), string(raw), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PutCatalogAsset(ctx context.Context, a catalog.Asset) error {
	_, err := s.db.ExecContext(ctx, s.q("INSERT INTO catalog_assets(id,media_type,data) VALUES(?,?,?) ON CONFLICT(id) DO NOTHING"), a.ID, a.MediaType, base64.StdEncoding.EncodeToString(a.Data))
	return err
}

func (s *Store) CatalogAsset(ctx context.Context, id string) (catalog.Asset, error) {
	a := catalog.Asset{ID: id}
	var data string
	err := s.db.QueryRowContext(ctx, s.q("SELECT media_type,data FROM catalog_assets WHERE id=?"), id).Scan(&a.MediaType, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	a.Data, err = base64.StdEncoding.DecodeString(data)
	return a, err
}
