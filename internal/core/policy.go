package core

import (
	"bufio"
	"embed"
	"fmt"
	"regexp"
	"strings"
)

//go:embed blocklists/*.txt
var blocklistFS embed.FS

// Builtin blocklist names.
const (
	BlocklistRFC2142 = "builtin:rfc2142"
	BlocklistCommon  = "builtin:common"
)

// LoadBuiltinBlocklist returns the entries of a builtin list.
func LoadBuiltinBlocklist(name string) ([]string, error) {
	var file string
	switch name {
	case BlocklistRFC2142:
		file = "blocklists/rfc2142.txt"
	case BlocklistCommon:
		file = "blocklists/common.txt"
	default:
		return nil, fmt.Errorf("unknown builtin blocklist %q", name)
	}
	f, err := blocklistFS.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

// CheckResult is why a label was rejected. Empty reason means allowed.
type CheckResult struct {
	Reason  string // "" | "blocked" | "policy_violation"
	Message string
}

// Policy validates candidate labels for a zone. Zero value = no constraints
// beyond normalization.
type Policy struct {
	Name    string
	MinLen  int
	MaxLen  int
	Pattern *regexp.Regexp // matched against the whole label if set
	// AllowKinds restricts which kinds may be claimed via the API; empty
	// allows all. Blocklist checks only apply to KindTenant claims —
	// reserving "grafana" for the platform is the point of the list.
	AllowKinds []Kind
	blocked    map[string]struct{}
}

// NewPolicy builds a policy with the given blocklists loaded.
func NewPolicy(name string, minLen, maxLen int, pattern string, blocklists ...string) (*Policy, error) {
	p := &Policy{Name: name, MinLen: minLen, MaxLen: maxLen, blocked: map[string]struct{}{}}
	if pattern != "" {
		re, err := regexp.Compile("^(?:" + pattern + ")$")
		if err != nil {
			return nil, fmt.Errorf("policy pattern: %w", err)
		}
		p.Pattern = re
	}
	for _, bl := range blocklists {
		entries, err := LoadBuiltinBlocklist(bl)
		if err != nil {
			return nil, err
		}
		p.AddBlocked(entries...)
	}
	return p, nil
}

// DefaultTenantPolicy is what a zone gets unless configured otherwise:
// 2–63 chars, both builtin blocklists.
func DefaultTenantPolicy() *Policy {
	p, err := NewPolicy("default", 2, 63, "", BlocklistRFC2142, BlocklistCommon)
	if err != nil {
		panic(err) // embedded lists; cannot fail
	}
	return p
}

// AddBlocked adds custom entries (already-normalized labels).
func (p *Policy) AddBlocked(entries ...string) {
	for _, e := range entries {
		p.blocked[strings.ToLower(strings.TrimSpace(e))] = struct{}{}
	}
}

// IsBlocked reports whether a normalized label is on the blocklist.
func (p *Policy) IsBlocked(label string) bool {
	_, ok := p.blocked[label]
	return ok
}

// Check validates a normalized label for a claim of the given kind.
func (p *Policy) Check(label string, kind Kind) CheckResult {
	if len(p.AllowKinds) > 0 {
		ok := false
		for _, k := range p.AllowKinds {
			if k == kind {
				ok = true
				break
			}
		}
		if !ok {
			return CheckResult{Reason: "policy_violation", Message: fmt.Sprintf("kind %q not allowed in this zone", kind)}
		}
	}
	if kind == KindTenant {
		if p.IsBlocked(label) {
			return CheckResult{Reason: "blocked", Message: fmt.Sprintf("%q is a reserved name", label)}
		}
		if p.MinLen > 0 && len(label) < p.MinLen {
			return CheckResult{Reason: "policy_violation", Message: fmt.Sprintf("label shorter than %d characters", p.MinLen)}
		}
		if p.MaxLen > 0 && len(label) > p.MaxLen {
			return CheckResult{Reason: "policy_violation", Message: fmt.Sprintf("label longer than %d characters", p.MaxLen)}
		}
		if p.Pattern != nil && !p.Pattern.MatchString(label) {
			return CheckResult{Reason: "policy_violation", Message: "label does not match zone pattern"}
		}
	}
	return CheckResult{}
}

// Suggest proposes alternative labels for a taken/blocked base. Suggestions
// are candidates only — the caller must still check availability.
func Suggest(base string) []string {
	base = strings.TrimSpace(strings.ToLower(base))
	out := []string{}
	for _, s := range []string{
		base + "-app",
		base + "-hq",
		base + "-team",
		"get" + base,
		base + "-2",
	} {
		if s != base && len(s) <= 63 {
			out = append(out, s)
		}
	}
	return out
}
