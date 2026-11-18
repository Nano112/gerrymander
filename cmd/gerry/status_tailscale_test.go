package main

import "testing"

func TestServeTerminates443(t *testing.T) {
	trap := `https://macbook.example.ts.net (tailnet only)
|-- / proxy https+insecure://127.0.0.1:443
`
	if !serveTerminates443(trap) {
		t.Fatal("TLS-terminating :443 handler not flagged")
	}

	// passthrough + an 8443 app handler: the healthy shape must NOT flag
	healthy := `https://macbook.example.ts.net:8443 (tailnet only)
|-- / proxy http://127.0.0.1:51005

|-- tcp://macbook.example.ts.net:443 (TLS over TCP)
|--> tcp://127.0.0.1:443
`
	if serveTerminates443(healthy) {
		t.Fatal("false positive on passthrough + 8443 handler")
	}
}

func TestSplitDNSRoutes(t *testing.T) {
	out := `Split DNS Routes:
  - mac-mini                       -> 192.0.2.10
  - test                           -> 192.0.2.11
  - ts.net.                        -> 192.0.2.53

Search Domains:
`
	r := splitDNSRoutes(out)
	if _, ok := r["mac-mini"]; !ok {
		t.Fatalf("routes parsed wrong: %v", r)
	}
	if _, ok := r["ts.net"]; !ok {
		t.Fatalf("trailing dot not trimmed: %v", r)
	}
	if _, ok := r["nope"]; ok {
		t.Fatalf("invented a route: %v", r)
	}
	// The target is what makes a hijacked zone diagnosable.
	if r["test"] != "192.0.2.11" {
		t.Fatalf("target not captured: %q", r["test"])
	}
	// Nothing outside the section, and no bare-domain entries without one.
	if len(r) != 3 {
		t.Fatalf("expected 3 routes, got %d: %v", len(r), r)
	}
}
