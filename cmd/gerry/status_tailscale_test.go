package main

import "testing"

func TestServeTerminates443(t *testing.T) {
	trap := `https://macbook.stalk-dinosaur.ts.net (tailnet only)
|-- / proxy https+insecure://127.0.0.1:443
`
	if !serveTerminates443(trap) {
		t.Fatal("TLS-terminating :443 handler not flagged")
	}

	// passthrough + an 8443 app handler: the healthy shape must NOT flag
	healthy := `https://macbook.stalk-dinosaur.ts.net:8443 (tailnet only)
|-- / proxy http://127.0.0.1:51005

|-- tcp://macbook.stalk-dinosaur.ts.net:443 (TLS over TCP)
|--> tcp://127.0.0.1:443
`
	if serveTerminates443(healthy) {
		t.Fatal("false positive on passthrough + 8443 handler")
	}
}

func TestSplitDNSRoutes(t *testing.T) {
	out := `Split DNS Routes:
  - mac-mini                       -> 100.71.144.24
  - ts.net.                        -> 199.247.155.53

Search Domains:
`
	r := splitDNSRoutes(out)
	if !r["mac-mini"] || !r["ts.net"] || r["test"] {
		t.Fatalf("routes parsed wrong: %v", r)
	}
}
