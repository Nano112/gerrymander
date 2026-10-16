package manifest

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Nano112/gerrymander/catalog"
	"gopkg.in/yaml.v3"
)

// RoutingProject preserves legacy ownership unless a workspace is opted into.
func (m *Manifest) RoutingProject() string {
	if m.Workspace == "" {
		return m.Project
	}
	return m.Project + "@" + m.Workspace
}
func (m *Manifest) OwnerRef(service string) string { return m.RoutingProject() + "/" + service }

func stableID(prefix string, parts ...string) string {
	return fmt.Sprintf("%s:%x", prefix, sha256.Sum256([]byte(strings.Join(parts, "\x00"))))
}

func (m *Manifest) ApplicationID() string {
	if m.Application.ID != "" {
		return m.Application.ID
	}
	if catalog.ValidID("app:" + m.Project) {
		return "app:" + m.Project
	}
	return stableID("app", m.Project)
}

func (m *Manifest) ServiceID(key string) string {
	if m.Services[key].ID != "" {
		return m.Services[key].ID
	}
	id := m.ApplicationID() + ":" + key
	if catalog.ValidID("service:" + id) {
		return "service:" + id
	}
	return stableID("service", m.ApplicationID(), key)
}

func (m *Manifest) DeploymentID(key string) string {
	if id := m.Services[key].Deployment.ID; id != "" {
		return id
	}
	return stableID("deployment", m.ApplicationID(), m.ServiceID(key), m.environment(), m.Workspace, m.Zone)
}

func (m *Manifest) environment() string {
	if m.Environment == "" {
		return "development"
	}
	return m.Environment
}

// CatalogBundle extracts only public metadata, never backend credentials,
// supervised environment variables, commands or authorization grants.
func (m *Manifest) CatalogBundle() (catalog.Bundle, error) {
	a := m.Application
	a.ID = m.ApplicationID()
	a.Project = m.Project
	if a.Display.Name == "" {
		a.Display.Name = m.Project
	}
	b := catalog.Bundle{SchemaVersion: catalog.SchemaVersion, Application: a, Services: []catalog.Service{}, Capabilities: []catalog.Capabilities{}, Deployments: []catalog.Deployment{}, Resources: []catalog.Resource{}}
	keys := make([]string, 0, len(m.Services))
	for k := range m.Services {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		s := m.Services[key]
		display := s.Display
		if display.Name == "" {
			display.Name = key
		}
		b.Services = append(b.Services, catalog.Service{ID: m.ServiceID(key), ApplicationID: a.ID, Key: key, Role: s.Role, Display: display})
		if s.Capabilities != nil {
			c := *s.Capabilities
			c.ServiceID = m.ServiceID(key)
			b.Capabilities = append(b.Capabilities, c)
		}
		d := s.Deployment
		d.ID = m.DeploymentID(key)
		d.ApplicationID = a.ID
		d.ServiceID = m.ServiceID(key)
		d.Environment = m.environment()
		d.Workspace = m.Workspace
		d.Zone = m.Zone
		d.OwnerRef = m.OwnerRef(key)
		d.Hostnames = s.Hostnames
		d.Lifecycle = "registered"
		// URLs are environment-specific. Only exact hostnames can supply a default.
		if d.LaunchURL == "" {
			for _, h := range s.Hostnames {
				if !strings.HasPrefix(h, "*.") {
					d.LaunchURL = "https://" + h
					break
				}
			}
		}
		b.Deployments = append(b.Deployments, d)
	}
	keys = keys[:0]
	for k := range m.Resources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		r := m.Resources[key]
		r.ApplicationID = a.ID
		r.Key = key
		r.Environment = m.environment()
		r.Workspace = m.Workspace
		if r.ID == "" {
			r.ID = stableID("resource", a.ID, key, r.Environment, r.Workspace)
		}
		if r.Display.Name == "" {
			r.Display.Name = key
		}
		r.ServiceIDs = append([]string(nil), r.ServiceIDs...)
		for i, k := range r.ServiceIDs {
			if _, ok := m.Services[k]; ok {
				r.ServiceIDs[i] = m.ServiceID(k)
			}
		}
		b.Resources = append(b.Resources, r)
	}
	return b, b.Validate()
}

// Prepare imports relative logo files on the client, then returns uploadable
// YAML. The daemon never reads paths supplied in an API request.
func Prepare(path string, upload func([]byte) (string, error)) ([]byte, error) {
	m, err := Load(path)
	if err != nil {
		return nil, err
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return nil, err
	}
	importLogo := func(d *catalog.Display) error {
		if d.Logo == "" || catalog.ValidAssetID(d.Logo) {
			return nil
		}
		if filepath.IsAbs(d.Logo) {
			return fmt.Errorf("logo must be relative to the manifest directory")
		}
		path, err := filepath.EvalSymlinks(filepath.Join(base, d.Logo))
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(base, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("logo escapes manifest directory")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > catalog.MaxAssetBytes {
			return fmt.Errorf("logo must be a regular file up to 512 KiB")
		}
		data, err := io.ReadAll(io.LimitReader(f, catalog.MaxAssetBytes+1))
		if err != nil {
			return err
		}
		a, err := catalog.NewAsset(data)
		if err != nil {
			return err
		}
		id, err := upload(a.Data)
		if err != nil {
			return err
		}
		if id != a.ID {
			return fmt.Errorf("logo upload returned unexpected digest")
		}
		d.Logo = id
		return nil
	}
	if err := importLogo(&m.Application.Display); err != nil {
		return nil, err
	}
	for k, s := range m.Services {
		if err := importLogo(&s.Display); err != nil {
			return nil, fmt.Errorf("service %s: %w", k, err)
		}
		m.Services[k] = s
	}
	for k, r := range m.Resources {
		if err := importLogo(&r.Display); err != nil {
			return nil, fmt.Errorf("resource %s: %w", k, err)
		}
		m.Resources[k] = r
	}
	if _, err := m.CatalogBundle(); err != nil {
		return nil, err
	}
	return yaml.Marshal(m)
}
