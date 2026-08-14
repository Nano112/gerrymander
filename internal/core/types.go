// Package core holds gerrymander's pure domain model: types, label
// normalization, and the policy engine. Nothing in this package performs I/O.
package core

import (
	"encoding/json"
	"time"
)

// Kind classifies who or what an allocation belongs to. All kinds share one
// namespace per zone — that is the entire point.
type Kind string

const (
	KindTenant   Kind = "tenant"
	KindPlatform Kind = "platform"
	KindReserved Kind = "reserved"
	KindBlocked  Kind = "blocked"
)

// Source records how an allocation came to exist.
type Source string

const (
	SourceAPI      Source = "api"
	SourceCRD      Source = "crd"
	SourceManifest Source = "manifest"
	SourceObserved Source = "observed"
	SourceSeed     Source = "seed"
)

// State is the allocation lifecycle.
type State string

const (
	StatePending   State = "pending"
	StateActive    State = "active"
	StateSuspended State = "suspended"
	StateReleasing State = "releasing"
	StateReleased  State = "released"
	StateFailed    State = "failed"
)

// Condition types, in the Kubernetes idiom.
const (
	CondAccepted   = "Accepted"
	CondDNSReady   = "DNSReady"
	CondRouteReady = "RouteReady"
	CondReady      = "Ready"
	CondConflict   = "Conflict"
)

// Zone is a namespace of labels (e.g. "olsyn.com", "olsyn.test").
type Zone struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Profile        string    `json:"profile"` // "prod" | "dev"
	WildcardMode   bool      `json:"wildcard_mode"`
	DNSProvider    string    `json:"dns_provider"`     // "none" | "embedded"
	IngressProvide string    `json:"ingress_provider"` // "none" | "embedded"
	PolicyName     string    `json:"policy"`
	CreatedAt      time.Time `json:"created_at"`
}

// ServiceBackend targets a Kubernetes service (used by observed allocations
// and, later, the traefik provider).
type ServiceBackend struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Port      int    `json:"port"`
}

// AddressBackend targets a host:port. Port may reference a sticky pool
// allocation instead of a literal value.
type AddressBackend struct {
	Host string `json:"host"`           // e.g. "olsyn-app", "127.0.0.1"
	Port int    `json:"port,omitempty"` // literal port
	// PortPool, when set, resolves the port from the owner's sticky
	// allocation in this pool at claim time.
	PortPool string `json:"port_pool,omitempty"`
	// Scheme of the upstream; "http" (default) or "https".
	Scheme string `json:"scheme,omitempty"`
	// PreserveHost forwards the original Host header upstream.
	PreserveHost bool `json:"preserve_host,omitempty"`
}

// SupervisedBackend is a process gerry starts on demand and sleeps when idle.
type SupervisedBackend struct {
	Cmd         string            `json:"cmd"`
	Dir         string            `json:"dir"`
	Env         map[string]string `json:"env,omitempty"`
	PortPool    string            `json:"port_pool,omitempty"` // ${PORT} source
	IdleTimeout Duration          `json:"idle_timeout,omitempty"`
	Health      *HealthCheck      `json:"health,omitempty"`
}

// HealthCheck is polled while a supervised process boots.
type HealthCheck struct {
	Path    string   `json:"path"`
	Timeout Duration `json:"timeout,omitempty"`
}

// Backend is what an allocation points at. Exactly one field is set.
type Backend struct {
	Kind       string             `json:"kind"` // "service" | "address" | "supervised"
	Service    *ServiceBackend    `json:"service,omitempty"`
	Address    *AddressBackend    `json:"address,omitempty"`
	Supervised *SupervisedBackend `json:"supervised,omitempty"`
}

// Route binds a listener port to a backend. An allocation carries one or
// more routes; the default route listens on 443/80. Extra listeners exist
// for dev parity (e.g. Vite HMR on :5175 over TLS).
type Route struct {
	Listen  int     `json:"listen"` // 0 = default listeners (80/443)
	Backend Backend `json:"backend"`
}

// Spec is the desired state of an allocation.
type Spec struct {
	Routes []Route `json:"routes,omitempty"`
	// Wildcard also claims "*.<label>.<zone>" (or "*.<zone>" when the
	// label is "@").
	Wildcard bool `json:"wildcard,omitempty"`
	// Priority for generated ingress routes; 0 means provider default.
	Priority int `json:"priority,omitempty"`
}

// ConditionStatus is one observed condition.
type ConditionStatus struct {
	Type    string    `json:"type"`
	Status  bool      `json:"status"`
	Reason  string    `json:"reason,omitempty"`
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at"`
}

// Status is the observed state of an allocation.
type Status struct {
	Conditions []ConditionStatus `json:"conditions,omitempty"`
}

// SetCondition upserts a condition by type.
func (s *Status) SetCondition(c ConditionStatus) {
	for i := range s.Conditions {
		if s.Conditions[i].Type == c.Type {
			s.Conditions[i] = c
			return
		}
	}
	s.Conditions = append(s.Conditions, c)
}

// Allocation is one claimed label in one zone.
type Allocation struct {
	ID        int64             `json:"id"`
	ZoneID    int64             `json:"zone_id"`
	ZoneName  string            `json:"zone,omitempty"`
	Project   string            `json:"project,omitempty"`
	Label     string            `json:"label"` // "@" = zone apex
	FQDN      string            `json:"fqdn"`
	Kind      Kind              `json:"kind"`
	Source    Source            `json:"source"`
	OwnerRef  string            `json:"owner_ref,omitempty"`
	OwnerKind string            `json:"owner_kind,omitempty"`
	State     State             `json:"state"`
	Spec      Spec              `json:"spec"`
	Status    Status            `json:"status"`
	Labels    map[string]string `json:"labels,omitempty"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"` // non-nil = hold
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// PortPool is a range of allocatable ports.
type PortPool struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	RangeStart int    `json:"range_start"`
	RangeEnd   int    `json:"range_end"`
	Avoid      []int  `json:"avoid,omitempty"`
}

// PortAllocation is one sticky (pool, owner) → value grant.
type PortAllocation struct {
	ID             int64     `json:"id"`
	PoolID         int64     `json:"pool_id"`
	PoolName       string    `json:"pool,omitempty"`
	Project        string    `json:"project,omitempty"`
	Value          int       `json:"value"`
	OwnerRef       string    `json:"owner_ref"`
	State          string    `json:"state"` // "active" | "occupied-foreign"
	LastVerifiedAt time.Time `json:"last_verified_at"`
}

// Duration marshals as a Go duration string ("30m") in JSON and YAML.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// UnmarshalYAML implements yaml.v3 unmarshalling.
func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

// FQDN builds the fully qualified name for a label in a zone. Label "@"
// means the zone apex.
func FQDN(label, zone string) string {
	if label == "@" || label == "" {
		return zone
	}
	return label + "." + zone
}
