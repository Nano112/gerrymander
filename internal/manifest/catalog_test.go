package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nano112/gerrymander/catalog"
)

func TestCatalogManifestIdentity(t *testing.T) {
	m, err := Parse([]byte("project: demo\nzone: demo.test\nservices:\n  web:\n    hostnames: [demo.test]\n    port_pool: dev\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.CatalogBundle()
	if err != nil {
		t.Fatal(err)
	}
	if b.Deployments[0].OwnerRef != "demo/web" || b.Deployments[0].Environment != "development" {
		t.Fatal(b)
	}
	first := b.Deployments[0].ID
	m.Workspace = "alice"
	b, err = m.CatalogBundle()
	if err != nil {
		t.Fatal(err)
	}
	if first == b.Deployments[0].ID || b.Deployments[0].OwnerRef != "demo@alice/web" {
		t.Fatal("workspace identity not isolated")
	}
	claims, err := m.Claims(func(_, owner string) (int, error) {
		if owner != "demo@alice/web" {
			t.Fatal(owner)
		}
		return 5555, nil
	})
	if err != nil || claims[0].Project != "demo@alice" {
		t.Fatalf("claims %v %v", claims, err)
	}
	m.Application.Display.Name = "New name"
	next, _ := m.CatalogBundle()
	if next.Application.ID != b.Application.ID || next.Services[0].ID != b.Services[0].ID {
		t.Fatal("display rename changed identity")
	}
}

func TestCatalogOnlyResourcesAndServices(t *testing.T) {
	m, err := Parse([]byte(`project: demo
application:
  id: app:demo
environment: production
services:
  api:
    id: api
    catalog_only: true
    deployment:
      launch_url: https://example.com
resources:
  db:
    kind: database
    services: [api]
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.CatalogBundle()
	if err != nil {
		t.Fatal(err)
	}
	claims, err := m.Claims(nil)
	if err != nil || len(claims) != 0 || b.Resources[0].ServiceIDs[0] != "api" {
		t.Fatalf("non-routed catalog: %v %v", b, err)
	}
}

func TestManifestLogoImports(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "gerrymander.yaml")
	logo := []byte(`<svg viewBox="0 0 10 10"><path d="M0 0L10 10"/></svg>`)
	os.WriteFile(filepath.Join(dir, "logo.svg"), logo, 0600)
	write := func(path string) {
		t.Helper()
		if err := os.WriteFile(file, []byte("project: demo\nzone: demo.test\napplication:\n  display:\n    logo: "+path+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("logo.svg")
	calls := 0
	upload := func(b []byte) (string, error) { calls++; a, err := catalog.NewAsset(b); return a.ID, err }
	raw, err := Prepare(file, upload)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(string(raw), "sha256:") {
		t.Fatal("logo not imported")
	}
	m, err := Parse(raw, "uploaded")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CatalogBundle(); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "logo.svg")
	os.WriteFile(outside, logo, 0600)
	os.Symlink(outside, filepath.Join(dir, "escape.svg"))
	for _, path := range []string{outside, "escape.svg", "../logo.svg"} {
		write(path)
		if _, err := Prepare(file, upload); err == nil {
			t.Fatalf("accepted outside path %s", path)
		}
	}
	if calls != 1 {
		t.Fatal("unsafe logo reached uploader")
	}
}
