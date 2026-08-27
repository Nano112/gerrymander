package main

import (
	"io"
	"log/slog"
	"os"
	"slices"
	"testing"

	"github.com/Nano112/gerrymander/internal/manifest"
)

func TestNeedsPort(t *testing.T) {
	cases := []struct {
		name string
		svc  manifest.Service
		want bool
	}{
		{"pool shorthand", manifest.Service{PortPool: "dev"}, true},
		{"supervised shorthand", manifest.Service{Supervised: &manifest.SupervisedSpec{Cmd: "x"}}, true},
		{"address shorthand", manifest.Service{Address: "127.0.0.1:9000"}, false},
		{"docker shorthand", manifest.Service{Docker: &manifest.DockerSpec{Network: "n", Host: "h", Port: 80}}, false},
		{"all-docker routes", manifest.Service{Routes: []manifest.RouteSpec{
			{Docker: &manifest.DockerSpec{Network: "n", Host: "h", Port: 80}},
			{Listen: 5175, Docker: &manifest.DockerSpec{Network: "n", Host: "h", Port: 5175}},
		}}, false},
		{"mixed routes", manifest.Service{Routes: []manifest.RouteSpec{
			{Docker: &manifest.DockerSpec{Network: "n", Host: "h", Port: 80}},
			{Listen: 5175, PortPool: "dev"},
		}}, true},
	}
	for _, c := range cases {
		if got := needsPort(c.svc); got != c.want {
			t.Errorf("%s: needsPort = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSubstPortLeavesPlaceholderWhenUngranted(t *testing.T) {
	if got := substPort("vite --port {PORT}", 51234); got != "vite --port 51234" {
		t.Errorf("granted: got %q", got)
	}
	// 0 must never be substituted: "--port 0" binds a random port silently.
	if got := substPort("vite --port {PORT}", 0); got != "vite --port {PORT}" {
		t.Errorf("ungranted: got %q", got)
	}
}

func TestDevEnvStripsInheritedPort(t *testing.T) {
	t.Setenv("PORT", "3000")
	t.Setenv("GERRY_PORT", "3000")

	if env := devEnv(0); slices.ContainsFunc(env, func(kv string) bool {
		return kv == "PORT=3000" || kv == "GERRY_PORT=3000"
	}) {
		t.Error("devEnv(0) leaked the inherited PORT to the child")
	}
	if env := devEnv(51234); !slices.Contains(env, "PORT=51234") {
		t.Error("devEnv(51234) did not export the grant")
	}
	// The rest of the environment must survive the filtering.
	if env := devEnv(0); len(env) < len(os.Environ())-2 {
		t.Errorf("devEnv(0) dropped %d unrelated vars", len(os.Environ())-2-len(env))
	}
}

func TestDockerSentinelPort(t *testing.T) {
	if p, ok := dockerSentinelPort("@docker", "4780"); !ok || p != "4780" {
		t.Errorf("bare sentinel: %q %v", p, ok)
	}
	if p, ok := dockerSentinelPort("@docker:5000", "4780"); !ok || p != "5000" {
		t.Errorf("explicit port: %q %v", p, ok)
	}
	if _, ok := dockerSentinelPort("127.0.0.1:4780", "4780"); ok {
		t.Error("a plain address matched the sentinel")
	}
}

func TestResolveExtraListenRefusesKeylessOffHost(t *testing.T) {
	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Loopback needs no key.
	got, err := resolveExtraListen(ctx, []string{"127.0.0.1:4781"}, "127.0.0.1:4780", "", false, "GERRY_API_KEY", log)
	if err != nil || !slices.Equal(got, []string{"127.0.0.1:4781"}) {
		t.Fatalf("loopback: %v %v", got, err)
	}
	// Off-host with no key is an open registry — refuse.
	if _, err := resolveExtraListen(ctx, []string{"0.0.0.0:4781"}, "127.0.0.1:4780", "", false, "GERRY_API_KEY", log); err == nil {
		t.Error("keyless 0.0.0.0 was accepted")
	}
	// ...unless a key is set, or it is explicitly allowed.
	if _, err := resolveExtraListen(ctx, []string{"0.0.0.0:4781"}, "127.0.0.1:4780", "secret", false, "GERRY_API_KEY", log); err != nil {
		t.Errorf("keyed 0.0.0.0: %v", err)
	}
	if _, err := resolveExtraListen(ctx, []string{"0.0.0.0:4781"}, "127.0.0.1:4780", "", true, "GERRY_API_KEY", log); err != nil {
		t.Errorf("allow_unauthenticated: %v", err)
	}
	// Duplicates and blanks collapse.
	got, err = resolveExtraListen(ctx, []string{"", "127.0.0.1:4781", " 127.0.0.1:4781 "}, "127.0.0.1:4780", "", false, "GERRY_API_KEY", log)
	if err != nil || !slices.Equal(got, []string{"127.0.0.1:4781"}) {
		t.Fatalf("dedupe: %v %v", got, err)
	}
}
