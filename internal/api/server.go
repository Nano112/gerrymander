// Package api exposes gerrymander's REST surface, health probes, and
// Prometheus metrics. Routing uses the Go 1.22+ stdlib pattern mux.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/service"
	"github.com/Nano112/gerrymander/internal/store"
)

// Metrics — names are part of the public contract (dashboards/alerts).
var (
	mAvailability = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gerry_availability_checks_total", Help: "Availability checks by result.",
	}, []string{"result"})
	mTransitions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gerry_allocation_transitions_total", Help: "Allocation state transitions.",
	}, []string{"from", "to"})
	mAllocations = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gerry_allocations", Help: "Current allocations.",
	}, []string{"zone", "kind", "state"})
	mPorts = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gerry_ports_allocated", Help: "Allocated ports per pool.",
	}, []string{"pool"})
	mConflicts = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gerry_conflicts", Help: "Unresolved conflicts per zone.",
	}, []string{"zone"})
	mUnregistered = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gerry_unregistered_observed_hosts", Help: "Observed hosts with no allocation.",
	}, []string{"zone"})
	mHoldsExpired = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gerry_holds_expired_total", Help: "Holds released by the reaper.",
	})
)

// ConflictReporter lets the observer publish findings through the API's
// /v1/conflicts endpoint without a hard dependency.
type ConflictReporter interface {
	Conflicts() []map[string]any
}

// Server carries the API dependencies.
type Server struct {
	Store  *store.Store
	Alloc  *service.Alloc
	Ports  *service.Ports
	APIKey string // empty = auth disabled (dev loopback)
	Log    *slog.Logger
	// Observer, when set, feeds /v1/conflicts.
	Observer ConflictReporter
	// ProcessCtl, when set, exposes supervisor endpoints.
	ProcessCtl ProcessController
	// OnMutation, when set, fires after any write that changes routing —
	// the proxy uses it to refresh its table without waiting for the poll.
	OnMutation func()
	// CAPEM, when set, is served at GET /v1/ca so `gerry trust` can fetch
	// the exact CA the proxy mints with (public material, unauthenticated).
	CAPEM []byte
	// HideMetrics keeps /metrics off this mux (it is being served on a
	// dedicated listener instead, so a public ingress can't reach it).
	HideMetrics bool
	// DNSInfo describes the embedded DNS server for the doctor.
	DNSInfo map[string]any
}

var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gerry_http_requests_total",
		Help: "API requests by matched route pattern, method and status code.",
	}, []string{"route", "method", "code"})
	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gerry_http_request_seconds",
		Help:    "API request latency by matched route pattern.",
		Buckets: []float64{.005, .025, .1, .5, 2},
	}, []string{"route"})
)

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }

// instrument records per-request metrics. Route label is the ServeMux
// pattern (bounded cardinality), never the raw URL.
func instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, code: 200}
		start := time.Now()
		next.ServeHTTP(sw, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		httpRequests.WithLabelValues(route, r.Method, strconv.Itoa(sw.code)).Inc()
		httpDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}

func (s *Server) mutated() {
	if s.OnMutation != nil {
		s.OnMutation()
	}
}

// ProcessController is implemented by the supervisor.
type ProcessController interface {
	List() []map[string]any
	Start(name string) error
	Stop(name string) error
	Logs(name string, lines int) ([]string, error)
}

// Handler builds the mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.Store.ListZones(r.Context()); err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		io.WriteString(w, "ok\n")
	})
	if !s.HideMetrics {
		mux.Handle("GET /metrics", promhttp.Handler())
	}
	serveCA := func(w http.ResponseWriter, r *http.Request) {
		if len(s.CAPEM) == 0 {
			writeErr(w, 404, "not_found", "this daemon runs no proxy / has no CA")
			return
		}
		// Phones decide what a download IS from these two headers: the
		// x509 content-type plus a .crt filename make iOS/Android offer
		// the install-certificate flow instead of a mystery file.
		w.Header().Set("Content-Type", "application/x-x509-ca-cert")
		w.Header().Set("Content-Disposition", `attachment; filename="gerrymander-root-ca.crt"`)
		w.Write(s.CAPEM)
	}
	mux.HandleFunc("GET /v1/ca", serveCA)
	mux.HandleFunc("GET /v1/ca.crt", serveCA) // extension in the URL too — some browsers trust that over headers

	mux.HandleFunc("GET /v1/zones", s.auth(s.handleZones))
	mux.HandleFunc("POST /v1/zones", s.adminOnly(s.handleCreateZone))
	mux.HandleFunc("DELETE /v1/zones/{zone}", s.adminOnly(s.handleDeleteZone))
	mux.HandleFunc("GET /v1/zones/{zone}/availability", s.auth(s.handleAvailability))
	mux.HandleFunc("POST /v1/claims", s.auth(s.handleClaim))
	mux.HandleFunc("POST /v1/allocations", s.auth(s.handleClaim)) // alias
	mux.HandleFunc("GET /v1/allocations", s.auth(s.handleListAllocations))
	mux.HandleFunc("GET /v1/allocations/{id}", s.auth(s.handleGetAllocation))
	mux.HandleFunc("PATCH /v1/allocations/{id}", s.auth(s.handlePatchAllocation))
	mux.HandleFunc("POST /v1/allocations/{id}/commit", s.auth(s.handleCommit))
	mux.HandleFunc("POST /v1/allocations/{id}/rename", s.auth(s.handleRename))
	mux.HandleFunc("POST /v1/allocations/{id}/renew", s.auth(s.handleRenew))
	mux.HandleFunc("DELETE /v1/allocations/{id}", s.auth(s.handleRelease))
	mux.HandleFunc("GET /v1/ports", s.adminOnly(s.handleListPorts))
	mux.HandleFunc("POST /v1/ports", s.adminOnly(s.handleClaimPort))
	mux.HandleFunc("POST /v1/manifest/apply", s.adminOnly(s.handleManifestApply))
	mux.HandleFunc("GET /v1/conflicts", s.adminOnly(s.handleConflicts))
	mux.HandleFunc("GET /v1/processes", s.adminOnly(s.handleProcesses))
	mux.HandleFunc("POST /v1/processes/{name}/start", s.adminOnly(s.handleProcessStart))
	mux.HandleFunc("POST /v1/processes/{name}/stop", s.adminOnly(s.handleProcessStop))
	mux.HandleFunc("GET /v1/processes/{name}/logs", s.adminOnly(s.handleProcessLogs))
	mux.HandleFunc("POST /v1/tokens", s.adminOnly(s.handleCreateToken))
	mux.HandleFunc("GET /v1/tokens", s.adminOnly(s.handleListTokens))
	mux.HandleFunc("DELETE /v1/tokens/{name}", s.adminOnly(s.handleRevokeToken))
	mux.HandleFunc("GET /v1/allocations/{id}/events", s.auth(s.handleAllocationEvents))
	mux.HandleFunc("GET /v1/dns", s.auth(func(w http.ResponseWriter, r *http.Request) {
		if s.DNSInfo == nil {
			writeJSON(w, 200, map[string]any{"enabled": false})
			return
		}
		writeJSON(w, 200, s.DNSInfo)
	}))

	return instrument(mux)
}

// Principal is who a request acts as, resolved from the bearer credential.
// The root API key (and keyless loopback dev mode) is an admin; scoped
// tokens from the tokens table may be confined to one owner_ref and a zone
// allowlist.
type Principal struct {
	Scope    string // store.ScopeAdmin | store.ScopeOwner
	Name     string // token name, or "root"
	OwnerRef string
	Zones    []string // empty = all zones
}

func (p Principal) admin() bool { return p.Scope == store.ScopeAdmin }

func (p Principal) zoneAllowed(zone string) bool {
	if len(p.Zones) == 0 {
		return true
	}
	for _, z := range p.Zones {
		if z == zone {
			return true
		}
	}
	return false
}

type principalKey struct{}

func principalFrom(r *http.Request) Principal {
	if p, ok := r.Context().Value(principalKey{}).(Principal); ok {
		return p
	}
	return Principal{Scope: store.ScopeAdmin, Name: "root"}
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := Principal{Scope: store.ScopeAdmin, Name: "root"}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case s.APIKey != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.APIKey)) == 1:
			// root key
		case strings.HasPrefix(got, "gk_"):
			tok, err := s.Store.LookupToken(r.Context(), got)
			if err != nil {
				writeErr(w, 401, "unauthorized", "unknown or revoked token")
				return
			}
			p = Principal{Scope: tok.Scope, Name: tok.Name, OwnerRef: tok.OwnerRef, Zones: tok.Zones}
		case s.APIKey == "":
			// keyless loopback dev mode (serve refuses this off-loopback)
		default:
			writeErr(w, 401, "unauthorized", "missing or invalid API key")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	}
}

// adminOnly gates endpoints that scoped tokens must never reach.
func (s *Server) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return s.auth(func(w http.ResponseWriter, r *http.Request) {
		if !principalFrom(r).admin() {
			writeErr(w, 403, "forbidden", "this endpoint requires an admin credential")
			return
		}
		next(w, r)
	})
}

// canTouchAllocation enforces owner-scope on by-id allocation operations.
// Admins pass without a lookup; owner tokens must own the allocation.
func (s *Server) canTouchAllocation(w http.ResponseWriter, r *http.Request, id int64) bool {
	p := principalFrom(r)
	if p.admin() {
		return true
	}
	a, err := s.Store.GetAllocation(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "not_found", "no such allocation")
		return false
	}
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return false
	}
	if a.OwnerRef != p.OwnerRef || !p.zoneAllowed(a.ZoneName) {
		writeErr(w, 403, "forbidden", "allocation belongs to a different owner")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, reason, msg string) {
	writeJSON(w, code, map[string]any{"error": reason, "message": msg})
}

func (s *Server) handleZones(w http.ResponseWriter, r *http.Request) {
	zones, err := s.Store.ListZones(r.Context())
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"zones": zones})
}

func (s *Server) handleCreateZone(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Profile string `json:"profile"` // "dev" | "prod" (default dev)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeErr(w, 400, "invalid", "body must be {\"name\": \"zone\", \"profile\": \"dev|prod\"}")
		return
	}
	if req.Profile == "" {
		req.Profile = "dev"
	}
	name := strings.ToLower(strings.Trim(req.Name, "."))
	z, err := s.Store.EnsureZone(r.Context(), core.Zone{Name: name, Profile: req.Profile, WildcardMode: true})
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 201, z)
}

func (s *Server) handleDeleteZone(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("zone")
	allocs, err := s.Store.ListAllocations(r.Context(), store.AllocFilter{Zone: name, ExcludeReleased: true})
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	if len(allocs) > 0 {
		writeErr(w, 409, "not_empty", fmt.Sprintf("zone %s still has %d allocation(s) — release them first", name, len(allocs)))
		return
	}
	if err := s.Store.DeleteZone(r.Context(), name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "not_found", "no such zone")
			return
		}
		writeErr(w, 500, "internal", err.Error())
		return
	}
	s.mutated()
	w.WriteHeader(204)
}

func (s *Server) handleAvailability(w http.ResponseWriter, r *http.Request) {
	zone := r.PathValue("zone")
	label := r.URL.Query().Get("label")
	av, err := s.Alloc.CheckAvailability(r.Context(), zone, label)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "not_found", err.Error())
			return
		}
		writeErr(w, 500, "internal", err.Error())
		return
	}
	result := "available"
	if !av.Available {
		result = av.Reason
	}
	mAvailability.WithLabelValues(result).Inc()
	writeJSON(w, 200, av)
}

func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	var req service.ClaimRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid", "bad JSON: "+err.Error())
		return
	}
	if p := principalFrom(r); !p.admin() {
		// Owner tokens claim tenant hostnames for themselves, nothing else.
		if req.OwnerRef != "" && req.OwnerRef != p.OwnerRef {
			writeErr(w, 403, "forbidden", "token cannot claim for another owner")
			return
		}
		if !p.zoneAllowed(req.Zone) {
			writeErr(w, 403, "forbidden", "token is not scoped to zone "+req.Zone)
			return
		}
		req.OwnerRef = p.OwnerRef
		req.OwnerKind = "tenant"
		req.Kind = core.KindTenant
		req.Source = core.SourceAPI
	}
	// Idempotency: replay the stored response verbatim.
	idem := r.Header.Get("Idempotency-Key")
	if idem != "" {
		if resp, ok, _ := s.Store.RecallIdempotent(r.Context(), idem); ok {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Idempotency-Replay", "true")
			w.WriteHeader(201)
			io.WriteString(w, resp)
			return
		}
	}
	resp, err := s.Alloc.Claim(r.Context(), req)
	if err != nil {
		var rej *service.ErrClaimRejected
		if errors.As(err, &rej) {
			code := 409
			if rej.Reason == "invalid" {
				code = 400
			}
			writeJSON(w, code, map[string]any{"error": rej.Reason, "message": rej.Message, "suggestions": rej.Suggestions})
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "not_found", err.Error())
			return
		}
		writeErr(w, 500, "internal", err.Error())
		return
	}
	if resp.Allocation != nil {
		mTransitions.WithLabelValues("", string(resp.Allocation.State)).Inc()
	}
	if idem != "" {
		body, _ := json.Marshal(resp)
		s.Store.RememberIdempotent(r.Context(), idem, string(body))
	}
	s.mutated()
	writeJSON(w, 201, resp)
}

func (s *Server) handleListAllocations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.AllocFilter{
		Zone: q.Get("zone"), Kind: q.Get("kind"), State: q.Get("state"),
		OwnerRef: q.Get("owner_ref"), Project: q.Get("project"), ExcludeReleased: true,
	}
	if p := principalFrom(r); !p.admin() {
		filter.OwnerRef = p.OwnerRef // owner tokens only ever see their own
	}
	allocs, err := s.Store.ListAllocations(r.Context(), filter)
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"allocations": allocs})
}

func (s *Server) pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "invalid", "bad allocation id")
		return 0, false
	}
	return id, true
}

func (s *Server) handleGetAllocation(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if !s.canTouchAllocation(w, r, id) {
		return
	}
	a, err := s.Store.GetAllocation(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "not_found", "no such allocation")
		return
	}
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, a)
}

func (s *Server) handlePatchAllocation(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if !s.canTouchAllocation(w, r, id) {
		return
	}
	a, err := s.Store.GetAllocation(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "not_found", "no such allocation")
		return
	}
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	var patch struct {
		Spec   *core.Spec         `json:"spec"`
		Labels *map[string]string `json:"labels"`
		State  *core.State        `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeErr(w, 400, "invalid", err.Error())
		return
	}
	if p := principalFrom(r); !p.admin() && (patch.State != nil || patch.Labels != nil) {
		writeErr(w, 403, "forbidden", "owner tokens may only patch spec")
		return
	}
	from := a.State
	if patch.Spec != nil {
		a.Spec = *patch.Spec
	}
	if patch.Labels != nil {
		a.Labels = *patch.Labels
	}
	if patch.State != nil {
		a.State = *patch.State
	}
	if err := s.Store.UpdateAllocation(r.Context(), a); err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	if patch.State != nil && from != a.State {
		mTransitions.WithLabelValues(string(from), string(a.State)).Inc()
	}
	s.mutated()
	writeJSON(w, 200, a)
}

func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if !s.canTouchAllocation(w, r, id) {
		return
	}
	a, err := s.Alloc.Commit(r.Context(), id)
	if err != nil {
		writeErr(w, 409, "conflict", err.Error())
		return
	}
	mTransitions.WithLabelValues("pending", "active").Inc()
	s.mutated()
	writeJSON(w, 200, a)
}

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if !s.canTouchAllocation(w, r, id) {
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Label == "" {
		writeErr(w, 400, "invalid", "body must be {\"label\": \"newname\"}")
		return
	}
	a, err := s.Alloc.Rename(r.Context(), id, body.Label)
	if err != nil {
		var rej *service.ErrClaimRejected
		if errors.As(err, &rej) {
			code := 409
			if rej.Reason == "invalid" {
				code = 400
			}
			writeJSON(w, code, map[string]any{"error": rej.Reason, "message": rej.Message, "suggestions": rej.Suggestions})
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "not_found", "no such allocation")
			return
		}
		writeErr(w, 500, "internal", err.Error())
		return
	}
	s.mutated()
	writeJSON(w, 200, a)
}

func (s *Server) handleRenew(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if !s.canTouchAllocation(w, r, id) {
		return
	}
	var body struct {
		TTL core.Duration `json:"ttl"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	a, err := s.Alloc.Renew(r.Context(), id, body.TTL.Std())
	if err != nil {
		writeErr(w, 409, "conflict", err.Error())
		return
	}
	writeJSON(w, 200, a)
}

func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if !s.canTouchAllocation(w, r, id) {
		return
	}
	if err := s.Alloc.Release(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "not_found", "no such allocation")
			return
		}
		writeErr(w, 500, "internal", err.Error())
		return
	}
	mTransitions.WithLabelValues("active", "released").Inc()
	s.mutated()
	w.WriteHeader(204)
}

func (s *Server) handleListPorts(w http.ResponseWriter, r *http.Request) {
	ports, err := s.Store.ListPorts(r.Context(), r.URL.Query().Get("pool"))
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ports": ports})
}

func (s *Server) handleClaimPort(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool     string `json:"pool"`
		Project  string `json:"project"`
		OwnerRef string `json:"owner_ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid", err.Error())
		return
	}
	if req.Pool == "" {
		req.Pool = "dev"
	}
	if req.OwnerRef == "" {
		writeErr(w, 400, "invalid", "owner_ref required")
		return
	}
	pa, err := s.Ports.Claim(r.Context(), req.Pool, req.Project, req.OwnerRef)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "not_found", err.Error())
			return
		}
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 201, pa)
}

func (s *Server) handleConflicts(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	if s.Observer != nil {
		out = s.Observer.Conflicts()
	}
	writeJSON(w, 200, map[string]any{"conflicts": out})
}

func (s *Server) handleProcesses(w http.ResponseWriter, r *http.Request) {
	if s.ProcessCtl == nil {
		writeJSON(w, 200, map[string]any{"processes": []any{}})
		return
	}
	writeJSON(w, 200, map[string]any{"processes": s.ProcessCtl.List()})
}

func (s *Server) handleProcessStart(w http.ResponseWriter, r *http.Request) {
	if s.ProcessCtl == nil {
		writeErr(w, 404, "not_found", "supervision disabled")
		return
	}
	if err := s.ProcessCtl.Start(r.PathValue("name")); err != nil {
		writeErr(w, 409, "conflict", err.Error())
		return
	}
	w.WriteHeader(202)
}

func (s *Server) handleProcessStop(w http.ResponseWriter, r *http.Request) {
	if s.ProcessCtl == nil {
		writeErr(w, 404, "not_found", "supervision disabled")
		return
	}
	if err := s.ProcessCtl.Stop(r.PathValue("name")); err != nil {
		writeErr(w, 409, "conflict", err.Error())
		return
	}
	w.WriteHeader(202)
}

func (s *Server) handleProcessLogs(w http.ResponseWriter, r *http.Request) {
	if s.ProcessCtl == nil {
		writeErr(w, 404, "not_found", "supervision disabled")
		return
	}
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if lines <= 0 {
		lines = 200
	}
	out, err := s.ProcessCtl.Logs(r.PathValue("name"), lines)
	if err != nil {
		writeErr(w, 404, "not_found", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"lines": out})
}

// RunGaugeRefresher keeps the allocation/port gauges current.
func (s *Server) RunGaugeRefresher(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.refreshGauges(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) refreshGauges(ctx context.Context) {
	allocs, err := s.Store.ListAllocations(ctx, store.AllocFilter{})
	if err != nil {
		return
	}
	mAllocations.Reset()
	for _, a := range allocs {
		mAllocations.WithLabelValues(a.ZoneName, string(a.Kind), string(a.State)).Inc()
	}
	ports, err := s.Store.ListPorts(ctx, "")
	if err != nil {
		return
	}
	mPorts.Reset()
	for _, p := range ports {
		mPorts.WithLabelValues(p.PoolName).Inc()
	}
}

// SetConflictGauge is called by the observer.
func SetConflictGauge(zone string, n int) { mConflicts.WithLabelValues(zone).Set(float64(n)) }

// SetUnregisteredGauge is called by the observer.
func SetUnregisteredGauge(zone string, n int) { mUnregistered.WithLabelValues(zone).Set(float64(n)) }

// IncHoldsExpired is called by the reaper wrapper.
func IncHoldsExpired(n int64) { mHoldsExpired.Add(float64(n)) }
