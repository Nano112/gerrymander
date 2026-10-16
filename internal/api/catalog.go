package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Nano112/gerrymander/catalog"
	"github.com/Nano112/gerrymander/internal/store"
)

func (s *Server) catalogRead(next http.HandlerFunc) http.HandlerFunc {
	return s.auth(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		p := principalFrom(r)
		if !p.admin() && p.Scope != store.ScopeCatalogRead && p.Scope != store.ScopeCatalogWrite {
			writeErr(w, 403, "forbidden", "catalog reads require a catalog or admin scope")
			return
		}
		next(w, r)
	})
}

func decodeCatalog(w http.ResponseWriter, r *http.Request, out any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*1024*1024))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}

func catalogError(w http.ResponseWriter, err error) {
	code, reason := 400, "invalid"
	if errors.Is(err, store.ErrCatalogConflict) {
		code, reason = 409, "conflict"
	}
	if errors.Is(err, store.ErrNotFound) {
		code, reason = 404, "not_found"
	}
	writeErr(w, code, reason, err.Error())
}

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.Catalog(r.Context(), r.URL.Query().Get("application_id"))
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) handleAccessCatalog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	env, workspace := q.Get("environment"), q.Get("workspace")
	if !catalog.ValidID(env) || (workspace != "" && !catalog.ValidID(workspace)) {
		writeErr(w, 400, "invalid", "environment is required; workspace must be a valid id")
		return
	}
	v, err := s.Store.Catalog(r.Context(), q.Get("application_id"))
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"schema_version": catalog.SchemaVersion, "services": v.Access(env, workspace)})
}

func (s *Server) handleApplyCatalog(w http.ResponseWriter, r *http.Request) {
	var b catalog.Bundle
	if err := decodeCatalog(w, r, &b); err != nil {
		catalogError(w, err)
		return
	}
	if err := s.Store.ApplyCatalog(r.Context(), b); err != nil {
		catalogError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"application_id": b.Application.ID, "schema_version": catalog.SchemaVersion})
}

func (s *Server) handleUploadCatalogAsset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Data []byte `json:"data"`
	}
	if err := decodeCatalog(w, r, &body); err != nil {
		catalogError(w, err)
		return
	}
	a, err := catalog.NewAsset(body.Data)
	if err != nil {
		catalogError(w, err)
		return
	}
	if err := s.Store.PutCatalogAsset(r.Context(), a); err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	a.Data = nil
	writeJSON(w, 201, a)
}

func (s *Server) handleCatalogAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !catalog.ValidAssetID(id) {
		writeErr(w, 400, "invalid", "invalid asset id")
		return
	}
	a, err := s.Store.CatalogAsset(r.Context(), id)
	if err != nil {
		catalogError(w, err)
		return
	}
	w.Header().Set("Content-Type", a.MediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("ETag", `"`+id+`"`)
	if r.Header.Get("If-None-Match") == `"`+id+`"` {
		w.WriteHeader(304)
		return
	}
	w.Write(a.Data)
}

func (s *Server) handleDeactivateCatalogDeployment(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeactivateCatalogDeployment(r.Context(), r.PathValue("id")); err != nil {
		catalogError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) catalogWrite(next http.HandlerFunc) http.HandlerFunc {
	return s.auth(func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r)
		if !p.admin() && p.Scope != store.ScopeCatalogWrite {
			writeErr(w, 403, "forbidden", "catalog publication requires admin or catalog-write scope")
			return
		}
		next(w, r)
	})
}
