package core

import (
	"math/rand"
	"strings"
	"testing"
)

func TestNormalizeBasics(t *testing.T) {
	cases := []struct {
		in   string
		kind Kind
		want string
		err  bool
	}{
		{"GV", KindTenant, "gv", false},
		{"  acme  ", KindTenant, "acme", false},
		{"@", KindPlatform, "@", false},
		{"rt.staging", KindPlatform, "rt.staging", false},
		{"rt.staging", KindTenant, "", true},   // dots are platform-only
		{"-acme", KindTenant, "", true},        // edge hyphen
		{"acme-", KindTenant, "", true},        // edge hyphen
		{"ac_me", KindTenant, "", true},        // bad rune
		{"a b", KindTenant, "", true},          // space
		{"", KindTenant, "", true},             // empty
		{strings.Repeat("a", 64), KindTenant, "", true}, // too long
		{"münchen", KindTenant, "xn--mnchen-3ya", false}, // IDN
		{"ＡＣＭＥ", KindTenant, "acme", false},              // NFKC fold of fullwidth
		{"*.cb.app", KindPlatform, "*.cb.app", false},    // platform wildcard
		{"*.foo", KindTenant, "", true},                  // tenant wildcard rejected
	}
	for _, c := range cases {
		got, err := Normalize(c.in, c.kind)
		if c.err {
			if err == nil {
				t.Errorf("Normalize(%q,%s): want error, got %q", c.in, c.kind, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Normalize(%q,%s): %v", c.in, c.kind, err)
			continue
		}
		if got != c.want {
			t.Errorf("Normalize(%q,%s) = %q, want %q", c.in, c.kind, got, c.want)
		}
	}
}

// The unique index is only honest if normalization is idempotent.
func TestNormalizeIdempotent(t *testing.T) {
	seeds := []string{"GV", "münchen", "ＡＣＭＥ", "rt.staging", "@", "*.cb.app", "foo-bar", "xn--mnchen-3ya"}
	for _, s := range seeds {
		for _, kind := range []Kind{KindTenant, KindPlatform} {
			once, err := Normalize(s, kind)
			if err != nil {
				continue
			}
			twice, err := Normalize(once, kind)
			if err != nil {
				t.Errorf("Normalize not re-accepting its own output: %q -> %q -> err %v", s, once, err)
				continue
			}
			if once != twice {
				t.Errorf("Normalize not idempotent: %q -> %q -> %q", s, once, twice)
			}
		}
	}
	// Randomized: any accepted output must re-normalize to itself.
	rng := rand.New(rand.NewSource(42))
	alphabet := []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.éüñ中文 ")
	for i := 0; i < 5000; i++ {
		n := 1 + rng.Intn(20)
		var b strings.Builder
		for j := 0; j < n; j++ {
			b.WriteRune(alphabet[rng.Intn(len(alphabet))])
		}
		in := b.String()
		for _, kind := range []Kind{KindTenant, KindPlatform} {
			once, err := Normalize(in, kind)
			if err != nil {
				continue
			}
			twice, err := Normalize(once, kind)
			if err != nil || once != twice {
				t.Fatalf("idempotency violated: %q -> %q -> (%q, %v)", in, once, twice, err)
			}
		}
	}
}

func TestPolicyBlocklists(t *testing.T) {
	p := DefaultTenantPolicy()
	for _, blocked := range []string{"www", "admin", "grafana", "api", "postmaster", "vpn", "staging"} {
		res := p.Check(blocked, KindTenant)
		if res.Reason != "blocked" {
			t.Errorf("Check(%q, tenant): want blocked, got %+v", blocked, res)
		}
	}
	// Platform claims bypass the blocklist — reserving these IS the point.
	if res := p.Check("grafana", KindPlatform); res.Reason != "" {
		t.Errorf("Check(grafana, platform): want allowed, got %+v", res)
	}
	if res := p.Check("acme", KindTenant); res.Reason != "" {
		t.Errorf("Check(acme, tenant): want allowed, got %+v", res)
	}
	if res := p.Check("a", KindTenant); res.Reason != "policy_violation" {
		t.Errorf("Check(a): want policy_violation (min len), got %+v", res)
	}
}

func TestPolicyPatternAndKinds(t *testing.T) {
	p, err := NewPolicy("strict", 2, 20, `[a-z][a-z0-9-]*`)
	if err != nil {
		t.Fatal(err)
	}
	p.AllowKinds = []Kind{KindTenant}
	if res := p.Check("9lives", KindTenant); res.Reason != "policy_violation" {
		t.Errorf("pattern should reject leading digit: %+v", res)
	}
	if res := p.Check("acme", KindPlatform); res.Reason != "policy_violation" {
		t.Errorf("AllowKinds should reject platform: %+v", res)
	}
}

func TestSuggest(t *testing.T) {
	s := Suggest("grafana")
	if len(s) == 0 {
		t.Fatal("no suggestions")
	}
	for _, x := range s {
		if x == "grafana" {
			t.Error("suggestion equals base")
		}
	}
}

func TestFQDN(t *testing.T) {
	if FQDN("gv", "olsyn.com") != "gv.olsyn.com" {
		t.Error("label fqdn")
	}
	if FQDN("@", "olsyn.com") != "olsyn.com" {
		t.Error("apex fqdn")
	}
}
