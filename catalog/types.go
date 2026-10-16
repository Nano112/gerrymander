// Package catalog defines Gerry's portable application/service catalog contract.
// It contains identity and capability definitions, never user grants or secrets.
package catalog

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const SchemaVersion = 1

type Display struct {
	Name        string            `json:"name" yaml:"name"`
	Description string            `json:"description,omitempty" yaml:"description,omitempty"`
	Logo        string            `json:"logo,omitempty" yaml:"logo,omitempty"` // sha256:<digest>; manifest clients may import a relative file
	Icon        string            `json:"icon,omitempty" yaml:"icon,omitempty"` // semantic icon key, e.g. postgres or gpu
	Color       string            `json:"color,omitempty" yaml:"color,omitempty"`
	Links       map[string]string `json:"links,omitempty" yaml:"links,omitempty"`
}

type Application struct {
	ID      string  `json:"id" yaml:"id"`
	Project string  `json:"project" yaml:"-"`
	Display Display `json:"display" yaml:"display"`
}

type Service struct {
	ID            string  `json:"id" yaml:"id"`
	ApplicationID string  `json:"application_id" yaml:"-"`
	Key           string  `json:"key" yaml:"-"`
	Role          string  `json:"role,omitempty" yaml:"role,omitempty"`
	Display       Display `json:"display" yaml:"display"`
}

// Capabilities is immutable once published under (service_id, version).
// A deployment explicitly selects a version; publication does not activate it.
type Capabilities struct {
	ServiceID       string              `json:"service_id" yaml:"-"`
	Version         string              `json:"version" yaml:"version"`
	EntryPermission string              `json:"entry_permission" yaml:"entry_permission"`
	Permissions     map[string]string   `json:"permissions" yaml:"permissions"`
	Roles           map[string][]string `json:"roles,omitempty" yaml:"roles,omitempty"`
}

type Deployment struct {
	Checks            []HTTPCheck `json:"checks,omitempty" yaml:"checks,omitempty"`
	ID                string      `json:"id" yaml:"id,omitempty"`
	ApplicationID     string      `json:"application_id" yaml:"-"`
	ServiceID         string      `json:"service_id" yaml:"-"`
	Environment       string      `json:"environment" yaml:"-"`
	Workspace         string      `json:"workspace,omitempty" yaml:"-"`
	Zone              string      `json:"zone,omitempty" yaml:"-"`
	OwnerRef          string      `json:"owner_ref,omitempty" yaml:"-"`
	Hostnames         []string    `json:"hostnames,omitempty" yaml:"-"`
	LaunchURL         string      `json:"launch_url,omitempty" yaml:"launch_url,omitempty"`
	CapabilityVersion string      `json:"capability_version,omitempty" yaml:"capability_version,omitempty"`
	Lifecycle         string      `json:"lifecycle" yaml:"-"` // registered | inactive; never a health assertion
}

// Resource describes a declared, non-routed project dependency. Discovery of
// provider infrastructure is a separate concern from these curated definitions.
type Resource struct {
	ID            string   `json:"id" yaml:"id,omitempty"`
	ApplicationID string   `json:"application_id" yaml:"-"`
	Key           string   `json:"key" yaml:"-"`
	Kind          string   `json:"kind" yaml:"kind"`
	Environment   string   `json:"environment" yaml:"-"`
	Workspace     string   `json:"workspace,omitempty" yaml:"-"`
	Provider      string   `json:"provider,omitempty" yaml:"provider,omitempty"`
	NativeID      string   `json:"native_id,omitempty" yaml:"native_id,omitempty"`
	Display       Display  `json:"display" yaml:"display"`
	ServiceIDs    []string `json:"service_ids,omitempty" yaml:"services,omitempty"`
}

type Bundle struct {
	SchemaVersion int            `json:"schema_version"`
	Application   Application    `json:"application"`
	Services      []Service      `json:"services"`
	Capabilities  []Capabilities `json:"capabilities"`
	Deployments   []Deployment   `json:"deployments"`
	Resources     []Resource     `json:"resources"`
}

type Snapshot struct {
	SchemaVersion int            `json:"schema_version"`
	Applications  []Application  `json:"applications"`
	Services      []Service      `json:"services"`
	Capabilities  []Capabilities `json:"capabilities"`
	Deployments   []Deployment   `json:"deployments"`
	Resources     []Resource     `json:"resources"`
}

var identity = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,159}$`)
var assetID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var color = regexp.MustCompile(`^#[a-fA-F0-9]{6}$`)

func ValidID(id string) bool      { return identity.MatchString(id) }
func ValidAssetID(id string) bool { return assetID.MatchString(id) }

func ValidateDisplay(d Display) error {
	if strings.TrimSpace(d.Name) == "" || len(d.Name) > 160 || len(d.Description) > 4096 {
		return fmt.Errorf("display needs a name (1–160 bytes); description maximum 4096 bytes")
	}
	if d.Logo != "" && !ValidAssetID(d.Logo) {
		return fmt.Errorf("logo must reference an imported sha256 asset")
	}
	if d.Icon != "" && !ValidID(d.Icon) {
		return fmt.Errorf("invalid semantic icon key")
	}
	if d.Color != "" && !color.MatchString(d.Color) {
		return fmt.Errorf("color must be #RRGGBB")
	}
	for name, link := range d.Links {
		if len(name) > 80 || !WebURL(link) {
			return fmt.Errorf("invalid display link %q", name)
		}
	}
	return nil
}

func WebURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil
}

func (b Bundle) Validate() error {
	if b.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported catalog schema_version")
	}
	if !ValidID(b.Application.ID) || strings.TrimSpace(b.Application.Project) == "" || len(b.Application.Project) > 160 {
		return fmt.Errorf("application requires a stable id and project")
	}
	if err := ValidateDisplay(b.Application.Display); err != nil {
		return fmt.Errorf("application: %w", err)
	}
	if len(b.Services) > 200 || len(b.Deployments) > 400 || len(b.Resources) > 500 || len(b.Capabilities) > 200 {
		return fmt.Errorf("catalog bundle is too large")
	}
	services := map[string]bool{}
	keys := map[string]bool{}
	ids := map[string]bool{b.Application.ID: true}
	for _, s := range b.Services {
		if !ValidID(s.ID) || (strings.TrimSpace(s.Key) == "" || len(s.Key) > 160) || s.ApplicationID != b.Application.ID || ids[s.ID] || keys[s.Key] {
			return fmt.Errorf("invalid or duplicate service identity %q", s.ID)
		}
		if err := ValidateDisplay(s.Display); err != nil {
			return fmt.Errorf("service %s: %w", s.ID, err)
		}
		ids[s.ID] = true
		keys[s.Key] = true
		services[s.ID] = true
	}
	versions := map[string]bool{}
	for _, c := range b.Capabilities {
		key := c.ServiceID + "/" + c.Version
		if !services[c.ServiceID] || !ValidID(c.Version) || versions[key] || len(c.Permissions) == 0 || len(c.Permissions) > 200 {
			return fmt.Errorf("invalid capability identity %q", key)
		}
		versions[key] = true
		if _, ok := c.Permissions[c.EntryPermission]; !ok {
			return fmt.Errorf("entry_permission must exist in permissions")
		}
		for id, label := range c.Permissions {
			if !ValidID(id) || strings.TrimSpace(label) == "" || len(label) > 300 {
				return fmt.Errorf("invalid permission %q", id)
			}
		}
		for role, permissions := range c.Roles {
			if strings.TrimSpace(role) == "" || len(role) > 100 || len(permissions) > 200 {
				return fmt.Errorf("invalid role %q", role)
			}
			entry := false
			for _, p := range permissions {
				if _, ok := c.Permissions[p]; !ok {
					return fmt.Errorf("role %s references unknown permission %s", role, p)
				}
				entry = entry || p == c.EntryPermission
			}
			if len(permissions) > 0 && !entry {
				return fmt.Errorf("role %s must include entry_permission", role)
			}
		}
	}
	checkIDs := map[string]bool{}
	for _, d := range b.Deployments {
		if len(d.Checks) > 20 {
			return fmt.Errorf("too many HTTP checks")
		}
		for _, c := range d.Checks {
			if err := c.Validate(); err != nil {
				return err
			}
			if checkIDs[c.ID] {
				return fmt.Errorf("duplicate HTTP check id %s", c.ID)
			}
			checkIDs[c.ID] = true
		}
		if !ValidID(d.ID) || ids[d.ID] || d.ApplicationID != b.Application.ID || !services[d.ServiceID] || !ValidID(d.Environment) || (d.Workspace != "" && !ValidID(d.Workspace)) {
			return fmt.Errorf("invalid deployment %q", d.ID)
		}
		ids[d.ID] = true
		if d.Lifecycle != "registered" && d.Lifecycle != "inactive" {
			return fmt.Errorf("invalid deployment lifecycle")
		}
		if d.LaunchURL != "" && !WebURL(d.LaunchURL) {
			return fmt.Errorf("launch_url must be an HTTP(S) URL without credentials")
		}
		if d.CapabilityVersion != "" && !ValidID(d.CapabilityVersion) {
			return fmt.Errorf("invalid capability_version")
		}
	}
	for _, r := range b.Resources {
		if !ValidID(r.ID) || ids[r.ID] || !ValidID(r.Key) || r.ApplicationID != b.Application.ID || !ValidID(r.Kind) || !ValidID(r.Environment) || (r.Workspace != "" && !ValidID(r.Workspace)) {
			return fmt.Errorf("invalid resource %q", r.ID)
		}
		ids[r.ID] = true
		if err := ValidateDisplay(r.Display); err != nil {
			return fmt.Errorf("resource %s: %w", r.ID, err)
		}
		for _, id := range r.ServiceIDs {
			if !services[id] {
				return fmt.Errorf("resource %s references unknown service %s", r.ID, id)
			}
		}
	}
	return nil
}
