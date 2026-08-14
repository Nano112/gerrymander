package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Nano112/gerrymander/internal/store"
)

// handleCreateToken mints a scoped token. The plaintext appears in this
// response and never again.
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string   `json:"name"`
		Scope    string   `json:"scope"` // admin | owner (default owner)
		OwnerRef string   `json:"owner_ref"`
		Zones    []string `json:"zones"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid", err.Error())
		return
	}
	if req.Name == "" {
		writeErr(w, 400, "invalid", "name is required")
		return
	}
	if req.Scope == "" {
		req.Scope = store.ScopeOwner
	}
	tok, plaintext, err := s.Store.CreateToken(r.Context(), req.Name, req.Scope, req.OwnerRef, req.Zones)
	if errors.Is(err, store.ErrTaken) {
		writeErr(w, 409, "taken", "a token with that name exists")
		return
	}
	if err != nil {
		writeErr(w, 400, "invalid", err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"token": tok, "plaintext": plaintext})
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	toks, err := s.Store.ListTokens(r.Context())
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"tokens": toks})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	err := s.Store.RevokeToken(r.Context(), r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "not_found", "no live token by that name")
		return
	}
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	w.WriteHeader(204)
}

// handleAllocationEvents exposes the append-only audit trail for one
// allocation. Owner tokens see only their own allocations' history.
func (s *Server) handleAllocationEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if !s.canTouchAllocation(w, r, id) {
		return
	}
	events, err := s.Store.ListEventsForSubject(r.Context(), "allocation", id)
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"events": events})
}
