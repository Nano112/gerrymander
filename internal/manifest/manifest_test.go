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
    routes:
      - { address: olsyn-app:80 }
      - { listen: 5175, address: olsyn-app:5175 }
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
	if m.Project != "olsyn-web" || len(m.Services) != 2 {
		t.Fatalf("parse: %+v", m)
	}
	claims, err := m.Claims(func(pool, owner string) (int, error) { return 51000, nil })
	if err != nil {
		t.Fatal(err)
	}
	// app: one claim "@" with wildcard and two routes; hmr: one claim "hmr"
	if len(claims) != 2 {
		t.Fatalf("claims: %d %+v", len(claims), claims)
	}
	byOwner := map[string]Claim{}
	for _, c := range claims {
		byOwner[c.OwnerRef] = c
	}
	app := byOwner["olsyn-web/app"]
	if app.Label != "@" || !app.Spec.Wildcard || len(app.Spec.Routes) != 2 {
		t.Fatalf("app claim: %+v", app)
	}
	if app.Spec.Routes[0].Listen != 0 || app.Spec.Routes[0].Backend.Address.Host != "olsyn-app" {
		t.Fatalf("app default route: %+v", app.Spec.Routes[0])
	}
	if app.Spec.Routes[1].Listen != 5175 || app.Spec.Routes[1].Backend.Address.Port != 5175 {
		t.Fatalf("app vite route: %+v", app.Spec.Routes[1])
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
	bad("mixroutes.yaml", "project: p\nzone: x.test\nservices:\n  a:\n    hostnames: [x.test]\n    address: b:80\n    routes: [{address: c:80}]", "mixes")
	bad("emptyroute.yaml", "project: p\nzone: x.test\nservices:\n  a:\n    hostnames: [x.test]\n    routes: [{listen: 5175}]", "backend")
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
