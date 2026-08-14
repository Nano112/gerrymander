package core

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/text/unicode/norm"
)

// Normalization errors. The API maps these to 400/409 reasons.
var (
	ErrEmptyLabel   = errors.New("label is empty")
	ErrLabelTooLong = errors.New("label exceeds 63 characters")
	ErrEdgeHyphen   = errors.New("label must not start or end with a hyphen")
	ErrBadRune      = errors.New("label contains invalid characters")
	ErrDotsInLabel  = errors.New("multi-level labels are only allowed for platform allocations")
)

// Normalize canonicalizes a label before any uniqueness check. It must be
// idempotent: Normalize(Normalize(x)) == Normalize(x) for every accepted x.
//
// Steps: trim, NFKC fold, lowercase, IDN → punycode per DNS label, structural
// validation. Dots are permitted only for kind=platform (observed hosts like
// "rt.staging"); tenant labels are single DNS labels. The literal "@" denotes
// the zone apex and passes through untouched. A leading "*." survives for
// platform wildcard patterns.
func Normalize(label string, kind Kind) (string, error) {
	s := strings.TrimSpace(label)
	if s == "" {
		return "", ErrEmptyLabel
	}
	if s == "@" {
		return "@", nil
	}

	wildcard := false
	if strings.HasPrefix(s, "*.") {
		if kind != KindPlatform {
			return "", fmt.Errorf("%w: wildcard labels", ErrDotsInLabel)
		}
		wildcard = true
		s = strings.TrimPrefix(s, "*.")
	}

	s = norm.NFKC.String(s)
	s = strings.ToLower(s)

	if strings.Contains(s, ".") && kind != KindPlatform {
		return "", ErrDotsInLabel
	}

	parts := strings.Split(s, ".")
	for i, p := range parts {
		np, err := normalizeDNSLabel(p)
		if err != nil {
			return "", err
		}
		parts[i] = np
	}
	out := strings.Join(parts, ".")
	if wildcard {
		out = "*." + out
	}
	return out, nil
}

func normalizeDNSLabel(p string) (string, error) {
	if p == "" {
		return "", ErrEmptyLabel
	}
	// Punycode-encode non-ASCII labels. idna.Lookup enforces RFC 5891
	// lookup rules (rejects edge hyphens except xn--, invalid runes, length).
	if !isASCII(p) {
		enc, err := idna.Lookup.ToASCII(p)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrBadRune, err)
		}
		p = enc
	}
	if len(p) > 63 {
		return "", ErrLabelTooLong
	}
	if strings.HasPrefix(p, "-") || strings.HasSuffix(p, "-") {
		return "", ErrEdgeHyphen
	}
	for _, r := range p {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return "", fmt.Errorf("%w: %q", ErrBadRune, r)
	}
	return p, nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
