package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

const sample = `
project: olsyn-web
zone: olsyn.test
services:
  app:
    hostnames: [olsyn.test, "*.olsyn.test"]
    address: olsyn-app:80
  vite:
    hostnames: [olsyn.test, "*.olsyn.test"]
    listen: [5175]
    address: olsyn-app:5175
  hmr:
    hostnames: [hmr.olsyn.test]
    supervised:
      cmd: npm run dev -- --port ${PORT}
      dir: .
      idle_timeout: 30m
      health: { path: /, timeout: 30s }
`

func TestManifestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gerrymander.yaml")
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Project != "olsyn-web" || len(m.Services) != 3 {
		t.Fatalf("parse: %+v", m)
	}
	claims, err := m.Claims(func(pool, owner string) (int, error) { return 51000, nil })
	if err != nil {
		t.Fatal(err)
	}
	// app: one claim "@" with wildcard; vite: one claim "@" wildcard on 5175; hmr: one claim "hmr"
	if len(claims) != 3 {
		t.Fatalf("claims: %d %+v", len(claims), claims)
	}
	byOwner := map[string]Claim{}
	for _, c := range claims {
		byOwner[c.OwnerRef] = c
	}
	app := byOwner["olsyn-web/app"]
	if app.Label != "@" || !app.Spec.Wildcard || app.Spec.Routes[0].Listen != 0 || app.Spec.Routes[0].Backend.Address.Host != "olsyn-app" {
		t.Fatalf("app claim: %+v", app)
	}
	vite := byOwner["olsyn-web/vite"]
	if vite.Spec.Routes[0].Listen != 5175 || vite.Spec.Routes[0].Backend.Address.Port != 5175 {
		t.Fatalf("vite claim: %+v", vite)
	}
	hmr := byOwner["olsyn-web/hmr"]
	if hmr.Label != "hmr" || hmr.Spec.Routes[0].Backend.Kind != "supervised" {
		t.Fatalf("hmr claim: %+v", hmr)
	}
	if hmr.Spec.Routes[0].Backend.Supervised.IdleTimeout.Std().Minutes() != 30 {
		t.Fatalf("idle timeout: %+v", hmr.Spec.Routes[0].Backend.Supervised)
	}
}

func TestManifestValidation(t *testing.T) {
	dir := t.TempDir()
	bad := func(name, content, wantSub string) {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(content), 0o644)
		_, err := Load(p)
		if err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	bad("noproject.yaml", "zone: x.test\nservices: {}", "project")
	bad("nozone.yaml", "project: p\nservices: {}", "zone")
	bad("nobackend.yaml", "project: p\nzone: x.test\nservices:\n  a:\n    hostnames: [x.test]", "backend")
	bad("twobackends.yaml", "project: p\nzone: x.test\nservices:\n  a:\n    hostnames: [x.test]\n    address: b:80\n    supervised: {cmd: x, dir: .}", "backend")
}

func TestOutOfZoneHostnameRejectedAtClaims(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.yaml")
	os.WriteFile(p, []byte("project: p\nzone: x.test\nservices:\n  a:\n    hostnames: [y.other.test]\n    address: b:80"), 0o644)
	m, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Claims(nil); err == nil {
		t.Fatal("out-of-zone hostname should fail at Claims()")
	}
}
